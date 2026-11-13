package grade

import (
	"regexp"
	"strings"
)

// hintRules turn the n8n error messages teams hit most often into one plain next step. They are
// shown only on visible sample tests (hidden tests never show their error), and they never change a verdict.
var hintRules = []struct {
	re   *regexp.Regexp
	hint string
}{
	{regexp.MustCompile(`(?i)could not get parameter "?(sheetName|documentId)"?`),
		"A Google Sheets node has settings n8n can't use. Check its Operation is a real one (reading rows is 'Get Row(s)' = read, not 'getAll') and that the Document and Sheet are filled in."},
	{regexp.MustCompile(`(?i)column names were updated after the node's setup`),
		"The columns set in a Google Sheets node don't match the sheet's header row. Use 'Map Automatically', or make the node's columns match your header exactly."},
	{regexp.MustCompile(`(?i)no respond to webhook node found|respond to webhook.*(not|never)`),
		"The webhook must answer. Add a 'Respond to Webhook' node at the end of each path, and set the Webhook node's Respond option to 'Using Respond to Webhook Node'."},
	{regexp.MustCompile(`(?i)cannot read propert(y|ies) of undefined \(reading '(body|[a-z_]+)'\)|cannot read propert(y|ies) of undefined`),
		"A node read a field that isn't there. The request arrives under $json.body (for example $json.body.roll_number), and a node may only see the previous node's output."},
	{regexp.MustCompile(`(?i)paired item data.*(unavailable|not available)|referenced node is unexecuted|node .* hasn't been executed`),
		"A node refers to another node's data that isn't available on this path. Use $('Node name').first().json.field, or pass the data forward with a Set or Code node."},
	{regexp.MustCompile(`(?i)(unrecognized|unknown) node type|nodeversionnotfound|node version`),
		"A node type or version isn't available on the judge (n8n 2.47.4). Re-add that node from the node panel."},
	{regexp.MustCompile(`(?i)credential.*(does not exist|not found|couldn't be found)`),
		"A node's credential is missing. Make sure the node has a credential selected before you export."},
	{regexp.MustCompile(`(?i)invalid json|json parameter needs to be valid json|unexpected token`),
		"Some JSON (a response body or a parameter) isn't valid. Open the node and check commas, quotes and expressions."},
	{regexp.MustCompile(`(?i)the workflow has issues and cannot be executed|has issues`),
		"A node is missing a required setting. Open each node with a red warning and fill in what it asks for."},
}

// Hint returns a short plain suggestion for an n8n error message, or "" if none applies.
func Hint(msg string) string {
	for _, r := range hintRules {
		if r.re.MatchString(msg) {
			return r.hint
		}
	}
	return ""
}

func withHint(err string) string {
	if err == "" || strings.Contains(err, "\nHint: ") {
		return err
	}
	if h := Hint(err); h != "" {
		return err + "\nHint: " + h
	}
	return err
}
