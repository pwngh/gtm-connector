package realdata

import (
	"fmt"
	"io"
	"path/filepath"
)

// shownDifferences is how many differences PrintReport lists; report.json lists up to maxListed.
const shownDifferences = 20

// PrintReport writes a report's counts and its first differences. It holds no text: items are named by
// dataset file and ID, or by line in a local run, and outputs by position and digest.
func PrintReport(w io.Writer, r *Report, run Run) {
	shown := r.PackageSHA256
	if len(shown) > 12 {
		shown = shown[:12]
	}
	fmt.Fprintf(w, "compare: %s against Python's %s (CPython %s, package %s), %d text items and %d JSON documents in %.1f s\n",
		r.Run, r.Against, r.Python, shown, r.TextItems, r.JSONItems, r.Seconds)
	fmt.Fprintf(w, "  %-24s %9s %10s %9s %12s\n", "function", "items", "non-empty", "matches", "differences")
	fmt.Fprintf(w, "  %-24s %9d %10s %9d %12d\n", "item (decoding the line)", r.Decoding.Items, "",
		r.Decoding.Matches, r.Decoding.Differences)
	for _, f := range r.Functions {
		fmt.Fprintf(w, "  %-24s %9d %10d %9d %12d\n", f.Function, f.Items, f.NonEmpty, f.Matches, f.Differences)
	}
	for _, k := range r.Kinds {
		if k.Kind != "" {
			fmt.Fprintf(w, "  %s: %d text items, %d JSON documents, %d outputs, %d differences\n", k.Kind,
				k.TextItems, k.JSONItems, k.Outputs, k.Differences)
		}
	}
	switch total := r.Total(); total {
	case 0:
		fmt.Fprintln(w, "  no differences")
	case 1:
		fmt.Fprintln(w, "  1 difference:")
	default:
		fmt.Fprintf(w, "  %d differences:\n", total)
	}
	for i, d := range r.Differences {
		if i == shownDifferences {
			fmt.Fprintf(w, "  and %d more listed in the report\n", len(r.Differences)-shownDifferences)
			break
		}
		where := fmt.Sprintf("line %d", d.Line)
		if !run.Local {
			where = fmt.Sprintf("%s %s (%s)", d.File, d.Item, d.Kind)
		}
		fmt.Fprintf(w, "  %s %s at %s; input %s, Python %s, Go %s\n", where, d.Function, d.Position,
			d.InputSHA256, short(d.PythonSHA256), short(d.GoSHA256))
	}
	if r.Unlisted > 0 {
		fmt.Fprintf(w, "  and %d counted but not listed\n", r.Unlisted)
	}
	fmt.Fprintf(w, "  report: %s\n", filepath.Join(run.Out, reportFile))
}

func short(digest string) string {
	if digest == "" {
		return "(none)"
	}
	if len(digest) > 16 {
		return digest[:16]
	}
	return digest
}
