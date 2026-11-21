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

	"github.com/pwngh/gtm-connector/internal/sources/echo"
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
	rows, err := o.query(soql, limit, nil)
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
// rows it sends, is refused, and so is a tenth page that is not the last. Each page is read and checked
// within its own request, and so is final, when it is given, with every row, on the last page.
func (o *operation) query(soql string, limit int, final func([]*strictjson.Object) error) ([]*strictjson.Object, error) {
	rows := []*strictjson.Object{}
	seenIDs, seenLocators := map[string]bool{}, map[string]bool{}
	path, first := queryPath, &soql
	var total *big.Int
	for page := 0; page < maxPages; page++ {
		finished := false
		_, err := o.request(path, first, func(value any) error {
			next, done, err := o.page(value, limit, &rows, &total, seenIDs, seenLocators)
			if err != nil || !done {
				path = next
				return err
			}
			finished = true
			if final != nil {
				return final(rows)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if finished {
			return rows, nil
		}
		first = nil
	}
	return nil, refuse(resultLimit)
}

// page checks one page of a query and adds its rows: it gives the next page's path, or whether this page
// was the last.
func (o *operation) page(value any, limit int, rowsOut *[]*strictjson.Object, totalOut **big.Int,
	seenIDs, seenLocators map[string]bool) (string, bool, error) {
	rows, total := *rowsOut, *totalOut
	defer func() { *rowsOut, *totalOut = rows, total }()
	{
		document, _ := value.(*strictjson.Object)
		doneValue, _ := document.Get("done")
		sizeValue, _ := document.Get("totalSize")
		recordsValue, _ := document.Get("records")
		done, isBool := doneValue.(bool)
		size, isInt := sizeValue.(*big.Int)
		records, isList := recordsValue.([]any)
		if document == nil || !isBool || !isInt || size.Sign() < 0 || !isList {
			return "", false, refuse(invalidResponse)
		}
		if total == nil {
			total = size
		}
		if size.Cmp(total) != 0 {
			return "", false, refuse(invalidResponse)
		}
		if total.Cmp(big.NewInt(int64(limit))) > 0 || len(rows)+len(records) > limit {
			return "", false, refuse(resultLimit)
		}
		for _, item := range records {
			row, ok := item.(*strictjson.Object)
			if !ok {
				return "", false, refuse(invalidResponse)
			}
			id, _ := row.Get("Id")
			native, err := nativeID(id)
			if err != nil || seenIDs[native] {
				return "", false, refuse(invalidResponse)
			}
			seenIDs[native] = true
			rows = append(rows, row)
		}
		next, _ := document.Get("nextRecordsUrl")
		if done {
			if int64(len(rows)) != total.Int64() || next != nil {
				return "", false, refuse(invalidResponse)
			}
			return "", true, nil
		}
		locator, ok := next.(string)
		if !ok || !locatorPattern.MatchString(locator) || seenLocators[locator] || len(records) == 0 ||
			int64(len(rows)) >= total.Int64() {
			return "", false, refuse(paginationInvalid)
		}
		seenLocators[locator] = true
		return locator, false, nil
	}
}

// request is one GET, and after a first HTTP 401 one more under the credentials the login source gives now:
// sf for an alias, or the variables of the process for a token handed to it. Before the
// retry the org and the user are read again and must be the ones this Reader saw before. The retry shares
// the operation's deadline, request count and byte budget.
func (o *operation) request(path string, soql *string, check func(any) error) (any, error) {
	document, err := o.requestOnce(path, soql, check)
	if !errors.Is(err, errUnauthorized) {
		return document, err
	}
	r := o.reader
	token, err := o.refresh()
	if err != nil {
		return nil, err
	}
	if r.editionVerified {
		_, err := o.query(organizationQuery, 1, r.validateEnvironment)
		if err == nil {
			err = o.checkUser()
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
	return o.requestOnce(path, soql, check)
}

// refresh asks the login source for credentials once in this operation. The wait for sf does not use up
// the read's own time. Credentials for another instance are refused, and so is the token the org has just
// refused. Variables of the process that are now missing or invalid refuse as an expired token does.
func (o *operation) refresh() (string, error) {
	r := o.reader
	switch {
	case o.refreshed:
		return "", refuse(authRequired)
	case !r.now().Before(o.deadline):
		return "", refuse(timedOut)
	case o.requests+3 > maxRequests:
		// No room is left to confirm the org and the user and retry, so sf is not run.
		return "", refuse(resultLimit)
	}
	o.refreshed = true
	started := r.now()
	given, err := r.credentials.read(o.ctx)
	o.deadline = o.deadline.Add(r.now().Sub(started))
	var failed *Error
	if errors.As(err, &failed) && failed.Code == invalidConfiguration && failed.Detail == "" {
		return "", refuse(authRequired)
	}
	if err != nil {
		return "", err
	}
	// A token that breaks the token rule is refused before it is held: it is never sent, and held it would
	// refuse every later answer that holds its text.
	if !echo.Usable(given.accessToken, maxToken) {
		return "", refuse(authRequired)
	}
	// Whatever usable token the source gave is held from here on, so no answer of this Reader may hold it,
	// sent or not.
	r.tokens.Add(given.accessToken)
	if origin, err := configure(given.instanceURL, given.accessToken); err != nil || origin != r.instanceURL {
		return "", refuse(authRequired)
	}
	if r.signedInTo != "" && (given.organizationID != r.signedInTo || given.username != r.username) ||
		given.accessToken == o.token {
		// The alias now names another org or another user, or the source gave back the token the org has
		// just refused. None is sent. A token handed to the process names no org or user ahead: the org and
		// the user it reads as are checked before it is kept.
		return "", refuse(authRequired)
	}
	o.token = given.accessToken
	return given.accessToken, nil
}

// errUserRefused is an HTTP 401 to the question of which user the token reads as. Its code is
// salesforce_auth_required. During the first check it starts the one replacement of the token.
var errUserRefused = &Error{Code: authRequired}

// userInfoPath is where a token says which user it reads as.
const userInfoPath = "/services/oauth2/userinfo"

// checkUser asks the org which user the operation's token reads as. It must be a user of the org this
// Reader checked, under the username sf org display named when sf named one, and, once a token has been
// checked, the same user ID as before. A refusal says only that the login cannot be used.
func (o *operation) checkUser() error {
	_, err := o.requestOnce(userInfoPath, nil, o.userIs)
	switch {
	case errors.Is(err, errUserMismatch):
		return refuse(authRequired)
	case errors.Is(err, errUnauthorized):
		// The org refused the token itself.
		return errUserRefused
	}
	return err
}

// errUserMismatch is a token that reads as another user than the one this Reader expects. Its code is
// salesforce_auth_required, and it starts no replacement of the token.
var errUserMismatch = &Error{Code: authRequired}

// errUnauthorized is an HTTP 401 from the org: the token was refused. Its code is salesforce_auth_required.
// A check of an answer that refuses with the same code is not one, and starts no replacement of the token.
var errUnauthorized = &Error{Code: authRequired}

// userIs checks the answer to which user the operation's token reads as.
func (o *operation) userIs(value any) error {
	r := o.reader
	document, _ := value.(*strictjson.Object)
	userValue, _ := document.Get("user_id")
	organizationValue, _ := document.Get("organization_id")
	username, _ := document.Get("preferred_username")
	userID, err := nativeID(userValue)
	if err != nil || !strings.HasPrefix(userID, "005") {
		return refuse(invalidResponse)
	}
	if organizationValue != r.organizationID || r.username != "" && username != r.username ||
		r.userID != "" && userID != r.userID {
		return errUserMismatch
	}
	r.userID = userID
	return nil
}

// requestOnce is one GET attempt and its checked JSON document, with check, when it is given, run on the
// document. A 200 must be labelled application/json and must not be compressed. With Options.Audit set it
// runs under a receipt of its step, finished with what the checks of its answer decide.
func (o *operation) requestOnce(path string, soql *string, check func(any) error) (any, error) {
	var document any
	err := audited(o.reader.audit, o.ctx, stepOf(path, soql), func(context.Context) error {
		var err error
		if document, err = o.attempt(path, soql); err == nil && check != nil {
			err = check(document)
		}
		return err
	})
	return document, err
}

// stepOf names a request for its receipt: org, userinfo, describe_OBJECT or query.
func stepOf(path string, soql *string) string {
	describe := "/services/data/" + apiVersion + "/sobjects/"
	switch {
	case path == userInfoPath:
		return "userinfo"
	case strings.HasPrefix(path, describe):
		object, _, _ := strings.Cut(strings.TrimPrefix(path, describe), "/")
		lowered := []byte(object)
		for i, c := range lowered {
			if c >= 'A' && c <= 'Z' {
				lowered[i] = c + 'a' - 'A'
			}
		}
		return "describe_" + string(lowered)
	case soql != nil && *soql == organizationQuery:
		return "org"
	}
	return "query"
}

// attempt is one GET and its JSON document.
func (o *operation) attempt(path string, soql *string) (any, error) {
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
	// The connection is not kept for another request, whatever transport carries this one.
	request.Close = true
	response, err := r.client.Do(request)
	if err != nil {
		return nil, o.transportError(ctx, err)
	}
	// The request is ended before its body is closed. net/http reads up to 256 KiB more of a body that is
	// closed early on a connection it means to keep, past the bytes the operation may take.
	defer func() {
		cancel()
		_ = response.Body.Close()
	}()
	status := response.StatusCode
	switch {
	case status == 401:
		return nil, errUnauthorized
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
	// An answer that holds a token of this operation is refused before it is parsed, so a token the org
	// sends back as a field never becomes a record.
	if o.reader.tokens.InBytes(body) {
		return nil, refuse(invalidResponse)
	}
	document, err := strictjson.LoadsDecimal(body)
	if err != nil {
		if status == 403 {
			return nil, refuse(forbidden)
		}
		return nil, refuse(invalidResponse)
	}
	// So is one whose parsed text holds a token, or would once a card composes it or lowers its case.
	if o.reader.tokens.InValue(document) {
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
		if _, err := o.describe(object.object, object.fields); err != nil {
			return err
		}
		passed[object.object] = true
	}
	for object := range passed {
		r.checked[object] = true
	}
	return nil
}

// describe is the field names of one queryable object. Each field of required must be among them, checked
// within the request.
func (o *operation) describe(object string, required []string) (map[string]bool, error) {
	var names map[string]bool
	_, err := o.request("/services/data/"+apiVersion+"/sobjects/"+object+"/describe", nil, func(value any) error {
		var err error
		if names, err = describedFields(object, value); err != nil {
			return err
		}
		for _, field := range required {
			if !names[field] {
				return refuse(schemaUnavailable)
			}
		}
		return nil
	})
	return names, err
}

// describedFields reads a describe answer: the field names of a queryable object.
func describedFields(object string, value any) (map[string]bool, error) {
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
