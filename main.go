package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
)

var (
	jsonFile = flag.String("file", "", "Path to the file with existing data.")
)

func mkPayload(limit int) map[string]interface{} {
	// I'm too lazy to write out types for this.
	return map[string]interface{}{
		"collection": map[string]interface{}{
			"id":      "4ddfb2b0-d852-4d08-8294-c2c227010358",
			"spaceId": "3434855b-af4b-426e-80fa-4f5994281327",
		},
		"collectionView": map[string]interface{}{
			"id":      "a35f82d0-6dbf-46d6-9ebb-aaf1fefa57a5",
			"spaceId": "3434855b-af4b-426e-80fa-4f5994281327",
		},
		"loader": map[string]interface{}{
			"type": "reducer",
			"reducers": map[string]interface{}{
				"collection_group_results": map[string]interface{}{
					"type":  "results",
					"limit": limit,
				},
			},
			"searchQuery":  "",
			"userTimeZone": "UTC",
		},
	}
}

type Error struct {
	ID      string `json:"errorId"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

type QueryCollectionResponse struct {
	Result    QueryResult `json:"result"`
	RecordMap RecordMap   `json:"recordMap"`
}

type QueryResult struct {
	SizeHint int `json:"sizeHint"`
}

type RecordMap struct {
	Block      map[string]Block              `json:"block"`
	Collection map[string]CollectionMetadata `json:"collection"`
}

type CollectionMetadata struct {
	Role  string                  `json:"role"`
	Value CollectionMetadataValue `json:"value"`
}

type CollectionMetadataValue struct {
	Schema map[string]SchemaField `json:"schema"`
}

type SchemaField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type Block struct {
	Role  string     `json:"role"`
	Value BlockValue `json:"value"`
}

type BlockValue struct {
	Type       string                   `json:"type"`
	Alive      bool                     `json:"alive"`
	Properties map[string][]interface{} `json:"properties"`
	ParentID   string                   `json:"parent_id"`
}

func queryColletion(limit int) (QueryCollectionResponse, error) {
	const url = "https://ukraine-dao.notion.site/api/v3/queryCollection?src=reset"

	b, err := json.Marshal(mkPayload(limit))
	if err != nil {
		return QueryCollectionResponse{}, fmt.Errorf("marshaling request payload: %w", err)
	}

	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))

	req.Header.Add("Accept", "application/json")
	req.Header.Add("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return QueryCollectionResponse{}, fmt.Errorf("sending the request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return QueryCollectionResponse{}, fmt.Errorf("reading body from an error response (code %d): %w", resp.StatusCode, err)
		}
		return QueryCollectionResponse{}, fmt.Errorf("request failed with code %d: %s", resp.StatusCode, body)
	}

	r := QueryCollectionResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return QueryCollectionResponse{}, fmt.Errorf("decoding response: %w", err)
	}
	return r, nil
}

func renameField(f string) string {
	switch f {
	case "Summary/Reason for being on this list":
		return "Summary"
	}
	return f
}

func useableDataFromResponse(resp QueryCollectionResponse) []map[string]string {
	collectionId := map[string]bool{}
	fieldName := map[string]string{}
	for k, c := range resp.RecordMap.Collection {
		collectionId[k] = true
		for k, f := range c.Value.Schema {
			fieldName[k] = renameField(f.Name)
		}
	}
	r := []map[string]string{}
	for _, b := range resp.RecordMap.Block {
		v := b.Value
		if v.Type != "page" || !collectionId[v.ParentID] {
			continue
		}
		entry := map[string]string{}
		for k, p := range v.Properties {
			entry[fieldName[k]] = notionCrapToString(p)
		}
		if strings.HasPrefix(entry["Twitter"], "@") {
			r = append(r, entry)
		}
	}
	return r
}

func notionCrapToString(crap []interface{}) string {
	builder := strings.Builder{}
	for _, turd := range crap {
		builder.WriteString(turdToString(turd))
	}
	return builder.String()
}

func turdToString(turd interface{}) string {
	l, ok := turd.([]interface{})
	if !ok {
		s, _ := json.Marshal(turd)
		return string(s)
	}
	switch len(l) {
	case 1:
		return ensureString(l[0])
	case 2:
		text := ensureString(l[0])
		link, ok := l[1].([]interface{})
		if !ok || len(link) != 1 {
			s, _ := json.Marshal(turd)
			return string(s)
		}
		link, ok = link[0].([]interface{})
		if !ok || len(link) != 2 || ensureString(link[0]) != "a" {
			s, _ := json.Marshal(turd)
			return string(s)
		}
		url := ensureString(link[1])
		tmpl := template.Must(template.New("link").Parse(`<a href="{{.URL}}">{{.Text}}</a>`))
		builder := &strings.Builder{}
		tmpl.Execute(builder, struct {
			Text string
			URL  string
		}{text, url})
		return builder.String()
	}
	s, _ := json.Marshal(turd)
	return string(s)
}

func ensureString(v interface{}) string {
	switch s := v.(type) {
	case string:
		return s
	default:
		r, _ := json.Marshal(v)
		return string(r)
	}
}

type List struct {
	// Wrapped in an object to allow adding more top-level metadata in the future
	// without breaking users.

	Entries []map[string]string `json:"entries"`
}

func mergeEntries(into map[string]string, from map[string]string) {
	for k, v := range from {
		if k == "id" {
			continue
		}
		into[k] = v
	}
}

func (l *List) Update(data []map[string]string) error {
	existingByUsername := map[string]map[string]string{}
	dupeIdxs := []int{}
	for i, e := range l.Entries {
		username := e["Twitter"]
		_, duplicate := existingByUsername[username]
		if duplicate {
			dupeIdxs = append(dupeIdxs, i)
		} else {
			existingByUsername[username] = e
		}
	}
	// TODO: handle dupes

	newEntries := []map[string]string{}
	for _, e := range data {
		existing, exists := existingByUsername[e["Twitter"]]
		if exists {
			mergeEntries(existing, e)
		} else {
			newEntries = append(newEntries, e)
		}
	}
	l.Entries = append(l.Entries, newEntries...)

	// TODO: lookup missing accout IDs

	return nil
}

func main() {
	resp, err := queryColletion(0)
	if err != nil {
		log.Fatalf("preflight request failed: %s", err)
	}
	resp, err = queryColletion(resp.Result.SizeHint)
	if err != nil {
		log.Fatalf("request failed: %s", err)
	}

	_, err = os.Stat(*jsonFile)
	if err != nil && !os.IsNotExist(err) {
		log.Fatalf("Failed to stat %q: %s", *jsonFile, err)
	}
	exists := err == nil
	data := &List{}
	if exists {
		f, err := os.Open(*jsonFile)
		if err != nil {
			log.Fatalf("Failed to open %q: %s", *jsonFile, err)
		}
		if err := json.NewDecoder(f).Decode(data); err != nil {
			log.Fatalf("Failed to unmarshal the content of %q: %s", *jsonFile, err)
		}
	}

	newData := useableDataFromResponse(resp)

	if err := data.Update(newData); err != nil {
		log.Fatalf("Failed to merge in new data: %s", err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		log.Fatalf("Failed to marshal data: %s", err)
	}
}
