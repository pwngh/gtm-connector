package salesforce

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// operation is the budget of one read: its deadline, its requests and its response bytes. Retries and
// pages share it.
type operation struct {
	reader   *Reader
	ctx      context.Context
	token    string
	deadline time.Time
	bytes    int
	requests int
	// refreshed is whether this operation has asked sf for credentials again. It may once.
	refreshed bool
	// unproved is a token sf gave before the org's first check passed. It is kept once that check passes.
	unproved string
}

// stopped is the error of a read whose context ended. It names the cause and nothing of the request.
func stopped(ctx context.Context) error {
	return fmt.Errorf("salesforce: the read was stopped: %w", context.Cause(ctx))
}

// records runs one query and maps each row.
func (o *operation) records(soql string, limit int,
	record func(*strictjson.Object) (*strictjson.Object, error)) ([]*strictjson.Object, error) {
	rows, err := o.query(soql, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*strictjson.Object, len(rows))
	for i, row := range rows {
		if out[i], err = record(row); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// query reads every page of one SOQL query. More than limit rows, by the total the org states or by the
// rows it sends, is refused, and so is a tenth page that is not the last.
func (o *operation) query(soql string, limit int) ([]*strictjson.Object, error) {
	rows := []*strictjson.Object{}
	seenIDs, seenLocators := map[string]bool{}, map[string]bool{}
	path, first := queryPath, &soql
	var total *big.Int
	for page := 0; page < maxPages; page++ {
		value, err := o.request(path, first)
		if err != nil {
			return nil, err
		}
		first = nil
		document, _ := value.(*strictjson.Object)
		doneValue, _ := document.Get("done")
		sizeValue, _ := document.Get("totalSize")
		recordsValue, _ := document.Get("records")
		done, isBool := doneValue.(bool)
		size, isInt := sizeValue.(*big.Int)
		records, isList := recordsValue.([]any)
		if document == nil || !isBool || !isInt || size.Sign() < 0 || !isList {
			return nil, refuse(invalidResponse)
		}
		if total == nil {
			total = size
		}
		if size.Cmp(total) != 0 {
			return nil, refuse(invalidResponse)
		}
		if total.Cmp(big.NewInt(int64(limit))) > 0 || len(rows)+len(records) > limit {
			return nil, refuse(resultLimit)
		}
		for _, item := range records {
			row, ok := item.(*strictjson.Object)
			if !ok {
				return nil, refuse(invalidResponse)
			}
			id, _ := row.Get("Id")
			native, err := nativeID(id)
			if err != nil || seenIDs[native] {
				return nil, refuse(invalidResponse)
			}
			seenIDs[native] = true
			rows = append(rows, row)
		}
		next, _ := document.Get("nextRecordsUrl")
		if done {
			if int64(len(rows)) != total.Int64() || next != nil {
				return nil, refuse(invalidResponse)
			}
			return rows, nil
		}
		locator, ok := next.(string)
		if !ok || !locatorPattern.MatchString(locator) || seenLocators[locator] || len(records) == 0 ||
			int64(len(rows)) >= total.Int64() {
			return nil, refuse(paginationInvalid)
		}
		seenLocators[locator] = true
		path = locator
	}
	return nil, refuse(resultLimit)
}

// request is one GET, and after a first HTTP 401 one more under the credentials sf gives now. Before the
// retry the org is read again and must be the one this Reader saw before. The retry shares the operation's
// deadline, request count and byte budget.
func (o *operation) request(path string, soql *string) (any, error) {
	document, err := o.requestOnce(path, soql)
	if codeOf(err) != authRequired {
		return document, err
	}
	r := o.reader
	token, err := o.refresh()
	if err != nil {
		return nil, err
	}
	if r.editionVerified {
		organization, err := o.query(organizationQuery, 1)
		if err == nil {
			err = r.validateEnvironment(organization)
		}
		switch codeOf(err) {
		case editionRequired, environmentMismatch, invalidResponse:
			return nil, refuse(authRequired)
		}
		if err != nil {
			return nil, err
		}
		// This credential has proved the existing identity.
		r.accessToken = token
	} else {
		// The first Organization answer is still to be checked before this credential may be kept.
		o.unproved = token
	}
	return o.requestOnce(path, soql)
}

// refresh asks sf for the alias's credentials once in this operation. The wait for sf does not use up the
// read's own time. Credentials for another instance are refused.
func (o *operation) refresh() (string, error) {
	r := o.reader
	switch {
	case o.refreshed:
		return "", refuse(authRequired)
	case !r.now().Before(o.deadline):
		return "", refuse(timedOut)
	case o.requests+2 > maxRequests:
		// No room is left to confirm the org and retry, so sf is not run.
		return "", refuse(resultLimit)
	}
	o.refreshed = true
	started := r.now()
	instanceURL, organizationID, accessToken, err := r.credentials.read(o.ctx)
	o.deadline = o.deadline.Add(r.now().Sub(started))
	if err != nil {
		return "", err
	}
	if origin, err := configure(instanceURL, accessToken); err != nil || origin != r.instanceURL {
		return "", refuse(authRequired)
	}
	if organizationID != r.signedInTo || accessToken == o.token {
		// The alias now names another org, or sf gave back the token the org has just refused. Neither
		// is sent.
		return "", refuse(authRequired)
	}
	o.token = accessToken
	return accessToken, nil
}

// requestOnce is one GET attempt and its checked JSON document. A 200 must be labelled application/json and
// must not be compressed.
func (o *operation) requestOnce(path string, soql *string) (any, error) {
	r := o.reader
	remaining := o.deadline.Sub(r.now())
	if remaining <= 0 {
		return nil, refuse(timedOut)
	}
	o.requests++
	if o.requests > maxRequests {
		return nil, refuse(resultLimit)
	}
	target := r.instanceURL + path
	if soql != nil {
		target += "?" + url.Values{"q": {*soql}}.Encode()
	}
	ctx, cancel := context.WithTimeout(o.ctx, min(requestTime, remaining))
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, refuse(unavailable)
	}
	request.Header.Set("Authorization", "Bearer "+o.token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	response, err := r.client.Do(request)
	if err != nil {
		return nil, o.transportError(ctx, err)
	}
	defer response.Body.Close()
	status := response.StatusCode
	switch {
	case status == 401:
		return nil, refuse(authRequired)
	case status == 429:
		return nil, refuse(rateLimited)
	case status != 200 && status != 403:
		return nil, refuse(httpError)
	case header(response, "Content-Encoding", "identity") != "identity":
		return nil, refuse(invalidResponse)
	}
	mediaType, _, _ := strings.Cut(header(response, "Content-Type", "application/json"), ";")
	if status == 200 && strings.Trim(mediaType, headerSpace) != "application/json" {
		// A page from a proxy or a sign-in wall is not a Salesforce answer, whatever its body parses to.
		return nil, refuse(invalidResponse)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(maxBytes-o.bytes)+1))
	o.bytes += len(body)
	switch {
	case o.bytes > maxBytes:
		return nil, refuse(resultLimit)
	case err != nil:
		return nil, o.transportError(ctx, err)
	case !r.now().Before(o.deadline):
		return nil, refuse(timedOut)
	}
	document, err := strictjson.LoadsDecimal(body)
	if err != nil {
		if status == 403 {
			return nil, refuse(forbidden)
		}
		return nil, refuse(invalidResponse)
	}
	if status == 403 {
		return nil, refuse(denial(document))
	}
	return document, nil
}

// headerSpace is the ASCII whitespace around a media type.
const headerSpace = "\t\n\v\f\r\x1c\x1d\x1e\x1f "

// header is a response header in ASCII lowercase, with repeated lines joined by ", ", or absent when the
// response has none.
func header(response *http.Response, name, absent string) string {
	values, ok := response.Header[name]
	if !ok {
		return absent
	}
	lowered := []byte(strings.Join(values, ", "))
	for i, c := range lowered {
		if c >= 'A' && c <= 'Z' {
			lowered[i] = c + ('a' - 'A')
		}
	}
	return string(lowered)
}

// denial is the code of an HTTP 403, by the error codes its body lists.
func denial(document any) string {
	codes := map[string]bool{}
	items, _ := document.([]any)
	for _, item := range items {
		if object, ok := item.(*strictjson.Object); ok {
			value, _ := object.Get("errorCode")
			if code, ok := value.(string); ok {
				codes[code] = true
			}
		}
	}
	switch {
	case codes["REQUEST_LIMIT_EXCEEDED"]:
		return rateLimited
	case codes["API_DISABLED_FOR_ORG"]:
		return apiDisabled
	}
	return forbidden
}

// transportError is the code of a request that got no usable answer. The transport's own error names the
// URL, so it is never passed on.
func (o *operation) transportError(request context.Context, err error) error {
	var network net.Error
	switch {
	case o.ctx.Err() != nil:
		return stopped(o.ctx)
	case request.Err() != nil, errors.As(err, &network) && network.Timeout():
		return refuse(timedOut)
	}
	return refuse(unavailable)
}

// checkCapabilities describes each object not yet checked: it must be queryable and expose the mapped
// fields. Nothing is kept from a check that fails part way.
func (o *operation) checkCapabilities() error {
	r := o.reader
	passed := map[string]bool{}
	for _, object := range required {
		if r.checked[object.object] {
			continue
		}
		names, err := o.describe(object.object)
		if err != nil {
			return err
		}
		for _, field := range object.fields {
			if !names[field] {
				return refuse(schemaUnavailable)
			}
		}
		passed[object.object] = true
	}
	for object := range passed {
		r.checked[object] = true
	}
	return nil
}

// describe is the field names of one queryable object.
func (o *operation) describe(object string) (map[string]bool, error) {
	value, err := o.request("/services/data/"+apiVersion+"/sobjects/"+object+"/describe", nil)
	if err != nil {
		return nil, err
	}
	document, _ := value.(*strictjson.Object)
	fieldsValue, _ := document.Get("fields")
	list, isList := fieldsValue.([]any)
	name, _ := document.Get("name")
	queryableValue, _ := document.Get("queryable")
	queryable, isBool := queryableValue.(bool)
	if document == nil || !isList || name != object || !isBool {
		return nil, refuse(invalidResponse)
	}
	if !queryable {
		return nil, refuse(forbidden)
	}
	names := map[string]bool{}
	for _, item := range list {
		field, _ := item.(*strictjson.Object)
		value, _ := field.Get("name")
		name, ok := value.(string)
		if !ok || !fieldPattern.MatchString(name) || names[name] {
			return nil, refuse(invalidResponse)
		}
		names[name] = true
	}
	return names, nil
}
