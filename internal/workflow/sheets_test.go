package workflow

import "testing"

func TestSheetHeadersFromDefineBelowSchema(t *testing.T) {
	w, err := Parse([]byte(`{"nodes":[
	 {"name":"A","type":"n8n-nodes-base.googleSheets","parameters":{"operation":"append","sheetName":{"__rl":true,"value":"Sheet1","mode":"name"},
	   "columns":{"mappingMode":"defineBelow","schema":[{"id":"roll_number"},{"id":"meal"},{"id":"gone","removed":true},{"id":"rating"}]}}},
	 {"name":"B","type":"n8n-nodes-base.googleSheets","parameters":{"operation":"append","sheetName":{"value":"Other","mode":"name"},
	   "columns":{"mappingMode":"autoMapInputData"}}},
	 {"name":"C","type":"n8n-nodes-base.googleSheets","parameters":{"operation":"read","sheetName":{"value":"Sheet1","mode":"name"}}}],"connections":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := w.SheetHeaders()
	if len(got) != 1 || len(got["Sheet1"]) != 3 || got["Sheet1"][0] != "roll_number" || got["Sheet1"][2] != "rating" {
		t.Fatalf("headers = %v", got)
	}
}
