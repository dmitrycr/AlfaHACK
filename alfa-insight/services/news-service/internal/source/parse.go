package source

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"github.com/andybalholm/cascadia"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/unicode/norm"
)

const AdapterVersion = "v1"

type Candidate struct {
	ExternalID  string `json:"external_id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Text        string `json:"text"`
	RawDate     string `json:"raw_date"`
	RawModified string `json:"raw_modified"`
	NeedsFetch  bool   `json:"needs_fetch"`
}
type Discovery struct {
	Items []Candidate
	Next  string
}

func Parse(s Source, body []byte, base string) (Discovery, error) {
	if len(body) > MaxBody {
		return Discovery{}, errors.New("response_too_large")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return Discovery{}, errors.New("empty_response")
	}
	if s.Kind == "html" && !bytes.HasPrefix(bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})), []byte("<")) {
		return Discovery{}, errors.New("invalid_html")
	}
	if bytes.Contains(bytes.ToUpper(body), []byte("<!DOCTYPE")) && (s.Kind == "rss" || s.Kind == "atom") {
		return Discovery{}, errors.New("xml_doctype_forbidden")
	}
	switch s.Kind {
	case "rss":
		return parseRSS(s, body, base)
	case "atom":
		return parseAtom(s, body, base)
	case "html":
		return parseHTMLList(s, body, base)
	case "json":
		return parseJSON(s, body, base)
	}
	return Discovery{}, errors.New("unsupported_adapter")
}
func parseRSS(s Source, b []byte, base string) (Discovery, error) {
	var feed struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Items []struct {
				ID          string `xml:"guid"`
				Link        string `xml:"link"`
				Title       string `xml:"title"`
				Description string `xml:"description"`
				Content     string `xml:"encoded"`
				Date        string `xml:"pubDate"`
				DCDate      string `xml:"date"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if e := decodeXML(b, &feed); e != nil {
		return Discovery{}, errors.New("invalid_rss")
	}
	d := Discovery{Items: []Candidate{}}
	for _, v := range feed.Channel.Items {
		content := first(v.Content, v.Description)
		d.Items = append(d.Items, Candidate{ExternalID: v.ID, URL: Resolve(base, v.Link), Title: Clean(v.Title), Text: Clean(content), RawDate: first(v.Date, v.DCDate), NeedsFetch: !s.FullText})
	}
	return d, nil
}
func parseAtom(s Source, b []byte, base string) (Discovery, error) {
	var feed struct {
		XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
		Entries []struct {
			ID        string `xml:"id"`
			Title     string `xml:"title"`
			Published string `xml:"published"`
			Updated   string `xml:"updated"`
			Summary   string `xml:"summary"`
			Content   struct {
				Value string `xml:",innerxml"`
			} `xml:"content"`
			Links []struct {
				Rel  string `xml:"rel,attr"`
				Href string `xml:"href,attr"`
				Type string `xml:"type,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if e := decodeXML(b, &feed); e != nil {
		return Discovery{}, errors.New("invalid_atom")
	}
	d := Discovery{Items: []Candidate{}}
	for _, v := range feed.Entries {
		link := ""
		for _, l := range v.Links {
			if (l.Rel == "alternate" || l.Rel == "") && (l.Type == "" || l.Type == "text/html") {
				link = l.Href
				break
			}
		}
		content := html.UnescapeString(v.Content.Value)
		content = strings.TrimSuffix(strings.TrimPrefix(content, "<![CDATA["), "]]>")
		d.Items = append(d.Items, Candidate{ExternalID: v.ID, URL: Resolve(base, link), Title: Clean(v.Title), Text: Clean(first(content, v.Summary)), RawDate: v.Published, RawModified: v.Updated, NeedsFetch: !s.FullText})
	}
	return d, nil
}
func decodeXML(b []byte, v any) error {
	decoder := xml.NewDecoder(bytes.NewReader(b))
	decoder.CharsetReader = charset.NewReaderLabel
	if e := decoder.Decode(v); e != nil {
		return e
	}
	for {
		token, e := decoder.Token()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		switch value := token.(type) {
		case xml.Comment, xml.ProcInst:
			continue
		case xml.CharData:
			if strings.TrimSpace(string(value)) == "" {
				continue
			}
		}
		return errors.New("unexpected XML trailing data")
	}
}
func document(b []byte, encoding string) (*xhtml.Node, error) {
	contentType := ""
	if encoding != "" {
		contentType = "text/html; charset=" + encoding
	}
	reader, e := charset.NewReader(bytes.NewReader(b), contentType)
	if e != nil {
		return nil, e
	}
	return xhtml.Parse(reader)
}
func query(n *xhtml.Node, selector string) *xhtml.Node {
	if selector == "" {
		return nil
	}
	s, e := cascadia.Compile(selector)
	if e != nil {
		return nil
	}
	if s.Match(n) {
		return n
	}
	return cascadia.Query(n, s)
}
func attr(n *xhtml.Node, key string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func text(n *xhtml.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk = func(v *xhtml.Node) {
		if v.Type == xhtml.ElementNode {
			switch v.Data {
			case "script", "style", "nav", "footer", "header", "aside", "noscript":
				return
			}
		}
		if v.Type == xhtml.TextNode {
			b.WriteString(v.Data)
			b.WriteByte(' ')
		}
		for c := v.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
func Clean(v string) string {
	n, e := xhtml.Parse(strings.NewReader(v))
	if e != nil {
		return strings.Join(strings.Fields(html.UnescapeString(v)), " ")
	}
	return norm.NFC.String(text(n))
}
func parseHTMLList(s Source, b []byte, base string) (Discovery, error) {
	n, e := document(b, s.Charset)
	if e != nil {
		return Discovery{}, e
	}
	selector, e := cascadia.Compile(s.Selectors["items"])
	if e != nil {
		return Discovery{}, e
	}
	d := Discovery{Items: []Candidate{}}
	for _, item := range cascadia.QueryAll(n, selector) {
		link := query(item, first(s.Selectors["link"], "a"))
		href := attr(link, "href")
		if href == "" {
			continue
		}
		d.Items = append(d.Items, Candidate{URL: Resolve(base, href), NeedsFetch: true})
	}
	d.Next = attr(query(n, s.Selectors["next"]), "href")
	if d.Next != "" {
		d.Next = Resolve(base, d.Next)
	}
	return d, nil
}
func ParseArticle(s Source, b []byte, base string) (Candidate, error) {
	n, e := document(b, s.Charset)
	if e != nil {
		return Candidate{}, e
	}
	v := Candidate{URL: base}
	if excluded := s.Selectors["exclude"]; excluded != "" {
		selector, err := cascadia.Compile(excluded)
		if err != nil {
			return v, err
		}
		for _, node := range cascadia.QueryAll(n, selector) {
			if node.Parent != nil {
				node.Parent.RemoveChild(node)
			}
		}
	}
	if title := query(n, s.Selectors["title"]); title != nil {
		v.Title = text(title)
	}
	if body := query(n, s.Selectors["body"]); body != nil {
		v.Text = text(body)
	}
	if date := query(n, s.Selectors["date"]); date != nil {
		v.RawDate = first(attr(date, "datetime"), attr(date, "content"), text(date))
	}
	jsonSelector, _ := cascadia.Compile(`script[type="application/ld+json"]`)
	for _, node := range cascadia.QueryAll(n, jsonSelector) {
		var raw strings.Builder
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == xhtml.TextNode {
				raw.WriteString(c.Data)
			}
		}
		var data any
		if json.Unmarshal([]byte(raw.String()), &data) != nil {
			continue
		}
		if obj := newsObject(data); obj != nil {
			v.Title = first(v.Title, str(obj["headline"]))
			v.Text = first(v.Text, Clean(str(obj["articleBody"])))
			v.RawDate = first(v.RawDate, str(obj["datePublished"]))
			v.RawModified = str(obj["dateModified"])
		}
	}
	v.Title = first(v.Title, attr(query(n, `meta[property="og:title"]`), "content"), text(query(n, "h1")))
	v.RawDate = first(v.RawDate, attr(query(n, `meta[property="article:published_time"]`), "content"))
	v.RawModified = first(v.RawModified, attr(query(n, `meta[property="article:modified_time"]`), "content"))
	if v.Text == "" {
		v.Text = text(query(n, "article"))
	}
	if v.Text == "" {
		return v, errors.New("article_body_not_found")
	}
	if canonical := attr(query(n, `link[rel="canonical"]`), "href"); canonical != "" {
		resolved := Resolve(base, canonical)
		if AllowedURL(resolved, s.AllowedHosts) == nil {
			v.URL = resolved
		}
	}
	v.Title = Clean(v.Title)
	return v, nil
}
func newsObject(v any) map[string]any {
	switch a := v.(type) {
	case []any:
		for _, x := range a {
			if o := newsObject(x); o != nil {
				return o
			}
		}
	case map[string]any:
		if str(a["@type"]) == "NewsArticle" || str(a["@type"]) == "Article" {
			return a
		}
		if kinds, ok := a["@type"].([]any); ok {
			for _, kind := range kinds {
				if kind == "NewsArticle" {
					return a
				}
			}
		}
		if graph, ok := a["@graph"]; ok {
			return newsObject(graph)
		}
	}
	return nil
}
func str(v any) string { s, _ := v.(string); return s }
func lookup(v any, path string) any {
	for _, part := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[part]
	}
	return v
}
func parseJSON(s Source, b []byte, base string) (Discovery, error) {
	if !utf8.Valid(b) {
		return Discovery{}, errors.New("json_encoding_not_utf8")
	}
	var data any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if e := decoder.Decode(&data); e != nil {
		return Discovery{}, errors.New("invalid_json")
	}
	if e := decoder.Decode(new(any)); e != io.EOF {
		return Discovery{}, errors.New("invalid_json_tail")
	}
	items, ok := lookup(data, s.JSONFields["items"]).([]any)
	if !ok {
		return Discovery{}, errors.New("json_items_not_array")
	}
	d := Discovery{Items: []Candidate{}}
	for _, item := range items {
		get := func(key string) string {
			v := lookup(item, s.JSONFields[key])
			if n, ok := v.(json.Number); ok {
				return string(n)
			}
			return str(v)
		}
		d.Items = append(d.Items, Candidate{ExternalID: get("id"), URL: Resolve(base, get("url")), Title: Clean(get("title")), Text: Clean(get("text")), RawDate: get("published_at"), RawModified: get("modified_at"), NeedsFetch: !s.FullText})
	}
	if path := s.JSONFields["next"]; path != "" {
		if next := str(lookup(data, path)); next != "" {
			d.Next = Resolve(base, next)
		}
	}
	return d, nil
}
func first(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
func ParseDate(raw string, s Source) (*time.Time, error) {
	if raw == "" {
		return nil, errors.New("missing_publication_date")
	}
	location, e := time.LoadLocation(s.Timezone)
	if e != nil {
		return nil, e
	}
	layouts := append([]string{time.RFC3339Nano, time.RFC1123Z, time.RFC1123, time.RFC822Z, "2006-01-02 15:04:05", "02.01.2006 15:04", "2006-01-02"}, s.DateLayouts...)
	for _, layout := range layouts {
		if t, e := time.ParseInLocation(layout, strings.TrimSpace(raw), location); e == nil {
			utc := t.UTC()
			return &utc, nil
		}
	}
	return nil, errors.New("invalid_publication_date")
}
func Normalize(s Source, c Candidate, scope, correlation string, now time.Time) (model.Article, error) {
	a := model.Article{SourceID: s.ID, ExternalID: c.ExternalID, OriginalURL: c.URL, Title: Clean(c.Title), Text: Clean(c.Text), RawDate: c.RawDate, CollectedAt: now.UTC(), Scope: scope, Origin: "source", Partial: c.NeedsFetch, AdapterVersion: AdapterVersion, ConfigVersion: s.Version, CorrelationID: correlation, Flags: []string{}}
	var e error
	a.URL, e = CanonicalURL(c.URL)
	if e != nil {
		return a, e
	}
	if e = AllowedURL(a.URL, s.AllowedHosts); e != nil {
		return a, e
	}
	if a.Title == "" || len([]rune(a.Title)) > 500 {
		return a, errors.New("invalid_title")
	}
	if a.Text == "" || len([]rune(a.Text)) > 200000 {
		return a, errors.New("invalid_text")
	}
	a.PublishedAt, e = ParseDate(c.RawDate, s)
	if e != nil {
		return a, e
	}
	if c.RawModified != "" {
		a.ModifiedAt, e = ParseDate(c.RawModified, s)
		if e != nil {
			return a, errors.New("invalid_modified_date")
		}
	}
	if a.PublishedAt.After(now.Add(5 * time.Minute)) {
		a.Flags = append(a.Flags, "future_publication")
	}
	if len([]rune(a.Text)) < 60 {
		a.Flags = append(a.Flags, "short_text")
	}
	if a.Partial {
		a.Flags = append(a.Flags, "partial_text")
	}
	a.ContentHash = platform.Hash(norm.NFC.String(strings.Join(strings.Fields(a.Title+"\n"+a.Text), " ")))
	a.DuplicateGroup = platform.Hash(scope + ":" + a.ContentHash)
	if a.ExternalID == "" {
		a.ExternalID = a.URL
	}
	return a, nil
}
