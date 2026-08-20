package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

type GatewayResource struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Fingerprint string    `gorm:"size:64;uniqueIndex;not null" json:"fingerprint"`
	Kind        string    `gorm:"size:24;index;index:idx_gateway_resource_kind_last_seen,priority:1;not null" json:"kind"`
	Name        string    `gorm:"size:255" json:"name"`
	MIMEType    string    `gorm:"size:120" json:"mimeType"`
	URI         string    `gorm:"size:2048" json:"uri"`
	SizeBytes   int64     `gorm:"not null;default:0" json:"sizeBytes"`
	SHA256      string    `gorm:"size:64" json:"sha256"`
	Preview     string    `gorm:"size:512" json:"preview"`
	PreviewData string    `gorm:"type:text" json:"previewData"`
	Sensitive   bool      `gorm:"not null;default:false;index" json:"sensitive"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `gorm:"index:idx_gateway_resource_last_seen,sort:desc;index:idx_gateway_resource_kind_last_seen,priority:2,sort:desc" json:"lastSeenAt"`
}

type RequestResource struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	RequestID  string `gorm:"size:36;index;index:idx_request_resource_resource_request,priority:2;uniqueIndex:uidx_request_resource_identity,priority:1;not null" json:"requestId"`
	ResourceID uint64 `gorm:"index:idx_request_resource_resource_request,priority:1;uniqueIndex:uidx_request_resource_identity,priority:2;not null" json:"resourceId"`
	Location   string `gorm:"size:512;uniqueIndex:uidx_request_resource_identity,priority:3;not null" json:"location"`
	Role       string `gorm:"size:32;not null" json:"role"`
	Occurrence int    `gorm:"not null;default:1" json:"occurrence"`
}

type ResourceView struct {
	GatewayResource
	Locations []string `json:"locations"`
}

type ResourceSummary struct {
	GatewayResource
	RequestCount int64 `json:"requestCount"`
	UseCount     int64 `json:"useCount"`
}

type ResourceQuery struct {
	Search         string
	Kind           string
	Sensitive      *bool
	LastSeenBefore *time.Time
	Page           int
	PageSize       int
}

type ResourceCatalogItem struct {
	ResourceSummary
	SessionCount int64 `json:"sessionCount"`
}

type ResourcePage struct {
	Items    []ResourceCatalogItem `json:"items"`
	Total    int64                 `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"pageSize"`
}

type ResourceSessionRef struct {
	TokenID           uint64 `json:"tokenId"`
	SessionID         string `json:"sessionId"`
	FallbackRequestID string `json:"fallbackRequestId"`
	SessionName       string `json:"sessionName"`
	RequestCount      int64  `json:"requestCount"`
}

type ResourceRequestRef struct {
	ID             string    `json:"id"`
	SessionID      string    `json:"sessionId"`
	SessionName    string    `json:"sessionName"`
	RequestedModel string    `json:"requestedModel"`
	Endpoint       string    `json:"endpoint"`
	CreatedAt      time.Time `json:"createdAt"`
	Location       string    `json:"location"`
}

type ResourceDetail struct {
	Resource GatewayResource      `json:"resource"`
	Sessions []ResourceSessionRef `json:"sessions"`
	Requests []ResourceRequestRef `json:"requests"`
}

type detectedResource struct {
	GatewayResource
	Location string
	Role     string
}

var httpURLPattern = regexp.MustCompile(`https?://[^\s"'<>\\{}\[\]]+`)

var (
	fileLineSuffixPattern           = regexp.MustCompile(`:\d+(?::\d+)?$`)
	fencedCodePattern               = regexp.MustCompile("(?s)```([A-Za-z0-9_+.-]*)[^\\r\\n]*\\r?\\n(.*?)```")
	inlineCodePattern               = regexp.MustCompile("`[^`\\r\\n]+`")
	codexSkillBlockPattern          = regexp.MustCompile(`(?is)<skill>[ \t\r\n]*(.*?)[ \t\r\n]*</skill>`)
	codexSkillNamePattern           = regexp.MustCompile(`(?is)<name>[ \t\r\n]*([^<\r\n]+?)[ \t\r\n]*</name>`)
	codexSkillPathPattern           = regexp.MustCompile(`(?is)<path>[ \t\r\n]*([^<\r\n]+?)[ \t\r\n]*</path>`)
	injectedMarkdownDocumentPattern = regexp.MustCompile(`(?ims)^(?:#{1,6}[ \t]+)?([A-Za-z0-9_.-]+\.md)[ \t]+instructions[ \t]+for[ \t]+([^\r\n]+?)[ \t]*\r?\n(?:[ \t]*\r?\n)*[ \t]*<INSTRUCTIONS>[ \t]*\r?\n(.*?)[ \t]*\r?\n?</INSTRUCTIONS>[ \t]*(?:\r?\n|$)`)
)

type detectedCodeSnippet struct {
	Content  string
	Language string
	MIMEType string
}

func detectResources(body []byte) []detectedResource {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil
	}
	result := make([]detectedResource, 0)
	walkResourceValue(value, "$", "request", &result)
	return coalesceDetectedSkillResources(result)
}

func walkResourceValue(value any, location, role string, result *[]detectedResource) {
	switch typed := value.(type) {
	case []any:
		for index, item := range typed {
			walkResourceValue(item, fmt.Sprintf("%s[%d]", location, index), role, result)
		}
	case map[string]any:
		currentRole := role
		if messageRole := strings.ToLower(strings.TrimSpace(stringValue(typed["role"]))); messageRole != "" {
			currentRole = messageRole
		}
		kind := strings.ToLower(strings.TrimSpace(stringValue(typed["type"])))
		if isToolOutputResourceType(kind) {
			currentRole = "tool"
		}
		if strings.Contains(kind, "image") {
			currentRole = "image"
		}
		inlineFileDetected := addDetectedInlineFile(typed, location, currentRole, result)
		for _, key := range []string{"url", "image_url", "file_url", "uri"} {
			if raw, ok := typed[key]; ok {
				if text, ok := raw.(string); ok {
					addDetectedResource(text, typed, fmt.Sprintf("%s.%s", location, key), currentRole, result)
				} else {
					itemRole := currentRole
					if key == "image_url" {
						itemRole = "image"
					}
					walkResourceValue(raw, fmt.Sprintf("%s.%s", location, key), itemRole, result)
				}
			}
		}
		for key, item := range typed {
			if key == "url" || key == "image_url" || key == "file_url" || key == "uri" || key == "file_id" || key == "fileId" || isExplicitFilePathKey(key) || isInlineFileDataKey(key) || (inlineFileDetected && isInlineFileContentKey(key)) {
				continue
			}
			itemRole := currentRole
			if key == "image_url" {
				itemRole = "image"
			}
			walkResourceValue(item, fmt.Sprintf("%s.%s", location, key), itemRole, result)
		}
	case string:
		remaining := extractCodexSkillResources(typed, location, role, result)
		remaining = extractInjectedMarkdownDocuments(remaining, location, role, result)
		allowUnfencedCode := role == "user" || role == "tool" || strings.HasSuffix(location, ".output") || strings.HasSuffix(location, ".source") || strings.HasSuffix(location, ".code")
		snippets := extractCodeSnippets(remaining, allowUnfencedCode)
		if shouldExtractTextURLs(role, location) {
			text := stripCodeForURLDetection(remaining)
			if _, _, sourceCode := inferCodeLanguage(text); !sourceCode {
				for _, token := range extractHTTPURLs(text) {
					addDetectedResource(token, nil, location, role, result)
				}
			}
		}
		for index, snippet := range snippets {
			addDetectedCodeResource(snippet, fmt.Sprintf("%s.code[%d]", location, index), role, result)
		}
	}
}

func extractCodexSkillResources(value, location, role string, result *[]detectedResource) string {
	remaining := value
	matches := codexSkillBlockPattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) > 0 {
		var builder strings.Builder
		last := 0
		for index, match := range matches {
			if len(match) < 4 {
				continue
			}
			builder.WriteString(value[last:match[0]])
			builder.WriteByte('\n')
			block := value[match[2]:match[3]]
			name := firstRegexpSubmatch(codexSkillNamePattern, block)
			reference := firstRegexpSubmatch(codexSkillPathPattern, block)
			addDetectedSkillResource(name, reference, fmt.Sprintf("%s.skill[%d]", location, index), role, result)
			last = match[1]
		}
		builder.WriteString(value[last:])
		remaining = builder.String()
	}

	if name, reference, consumed, ok := codexSkillInvocationPrefix(remaining); ok {
		addDetectedSkillResource(name, reference, location+".skill", role, result)
		remaining = remaining[consumed:]
	}
	return remaining
}

func codexSkillInvocationPrefix(value string) (string, string, int, bool) {
	consumed, ok := consumeCodexSkillPrefix(value)
	if !ok {
		return "", "", 0, false
	}
	segment := strings.TrimSpace(value[:consumed])
	if strings.HasPrefix(segment, "[$") {
		closeLabel := strings.Index(segment, "](")
		if closeLabel > 2 {
			name := segment[2:closeLabel]
			reference := strings.TrimSuffix(segment[closeLabel+2:], ")")
			return name, cleanFilePathCandidate(reference), consumed, true
		}
		if closeLabel := strings.IndexByte(segment, ']'); closeLabel > 2 {
			return segment[2:closeLabel], "", consumed, true
		}
	}
	token := strings.Trim(strings.Fields(segment)[0], "[](){}:;,.")
	return strings.TrimPrefix(token, "$"), "", consumed, true
}

func firstRegexpSubmatch(pattern *regexp.Regexp, value string) string {
	matches := pattern.FindStringSubmatch(value)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func addDetectedSkillResource(name, reference, location, role string, result *[]detectedResource) {
	name = strings.TrimSpace(strings.TrimPrefix(name, "$"))
	if !isCodexSkillInvocation("$" + name) {
		return
	}
	reference = cleanFilePathCandidate(reference)
	resource := detectedResource{Location: location, Role: role}
	resource.Kind = "skill"
	resource.Name = "$" + name
	resource.MIMEType = "application/x-codex-skill"
	resource.URI = "skill:" + name
	if reference != "" {
		resource.URI = truncateRunes(reference, 2048)
	}
	resource.Preview = "调用 " + resource.Name
	fingerprint := sha256.Sum256([]byte("skill|" + strings.ToLower(name)))
	resource.Fingerprint = hex.EncodeToString(fingerprint[:])
	*result = append(*result, resource)
}

func coalesceDetectedSkillResources(resources []detectedResource) []detectedResource {
	result := make([]detectedResource, 0, len(resources))
	positions := make(map[string]int)
	for _, resource := range resources {
		if resource.Kind != "skill" {
			result = append(result, resource)
			continue
		}
		if position, exists := positions[resource.Fingerprint]; exists {
			if strings.HasPrefix(result[position].URI, "skill:") && !strings.HasPrefix(resource.URI, "skill:") {
				result[position].URI = resource.URI
			}
			continue
		}
		positions[resource.Fingerprint] = len(result)
		result = append(result, resource)
	}
	return result
}

func extractInjectedMarkdownDocuments(value, location, role string, result *[]detectedResource) string {
	matches := injectedMarkdownDocumentPattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	var builder strings.Builder
	last := 0
	for index, match := range matches {
		if len(match) < 8 {
			continue
		}
		builder.WriteString(value[last:match[0]])
		builder.WriteByte('\n')
		name := strings.TrimSpace(value[match[2]:match[3]])
		root := strings.Trim(strings.TrimSpace(value[match[4]:match[5]]), "`\"'")
		content := strings.TrimSpace(value[match[6]:match[7]])
		if content != "" {
			addDetectedInjectedMarkdownDocument(name, root, content, fmt.Sprintf("%s.document[%d]", location, index), role, result)
		}
		last = match[1]
	}
	builder.WriteString(value[last:])
	return builder.String()
}

func addDetectedInjectedMarkdownDocument(name, root, content, location, role string, result *[]detectedResource) {
	reference := root
	if !strings.EqualFold(resourcePathBase(reference), name) {
		separator := "/"
		if strings.Contains(reference, `\`) && !strings.Contains(reference, "/") {
			separator = `\`
		}
		reference = strings.TrimRight(reference, `/\`) + separator + name
	}
	contentBytes := []byte(content)
	contentHash := sha256.Sum256(contentBytes)
	resource := detectedResource{Location: location, Role: role}
	resource.Kind = "file"
	resource.Name = name
	resource.MIMEType = "text/markdown"
	resource.URI = truncateRunes(reference, 2048)
	resource.SizeBytes = int64(len(contentBytes))
	resource.SHA256 = hex.EncodeToString(contentHash[:])
	resource.Preview = truncateRunes(content, 512)
	fingerprint := sha256.Sum256([]byte("file|" + reference + "|" + name + "|text/markdown"))
	resource.Fingerprint = hex.EncodeToString(fingerprint[:])
	*result = append(*result, resource)
}

func isToolOutputResourceType(kind string) bool {
	if kind == "function_call_output" || kind == "tool_result" || kind == "tool_output" || kind == "custom_tool_call_output" {
		return true
	}
	return strings.Contains(kind, "tool") && (strings.Contains(kind, "output") || strings.Contains(kind, "result"))
}

func shouldExtractTextURLs(role, location string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "user" && role != "request" {
		return false
	}
	lowerLocation := strings.ToLower(location)
	return !strings.Contains(lowerLocation, ".output") && !strings.Contains(lowerLocation, ".result")
}

func stripCodeForURLDetection(value string) string {
	value = fencedCodePattern.ReplaceAllString(value, " ")
	return inlineCodePattern.ReplaceAllString(value, " ")
}

func isExplicitFilePathKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(strings.TrimSpace(key)))
	switch normalized {
	case "path", "filepath", "filename", "localpath", "sourcepath":
		return true
	default:
		return false
	}
}

func cleanFilePathCandidate(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "`\"'()[]{}<>,;")
	value = fileLineSuffixPattern.ReplaceAllString(value, "")
	value = strings.TrimRight(value, ".,;:")
	return strings.TrimSpace(value)
}

func looksLikeFilePath(value string) bool {
	if value == "" || strings.ContainsAny(value, "\r\n\x00*?") || strings.HasPrefix(value, "//") {
		return false
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "file-") || strings.HasPrefix(lower, "file_") {
		return false
	}
	base := resourcePathBase(value)
	if base == "" || base == "." || base == ".." {
		return false
	}
	switch strings.ToLower(base) {
	case "dockerfile", "makefile", "rakefile", "gemfile", "procfile", "jenkinsfile", "cmakelists.txt":
		return true
	}
	extension := path.Ext(base)
	if len(extension) < 2 || len(extension) > 17 {
		return false
	}
	for _, char := range extension[1:] {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func resourcePathBase(value string) string {
	normalized := strings.ReplaceAll(value, `\`, "/")
	return path.Base(strings.TrimRight(normalized, "/"))
}

func sourceMIMETypeForPath(value string) string {
	base := strings.ToLower(resourcePathBase(value))
	if base == "dockerfile" {
		return "text/x-dockerfile"
	}
	switch path.Ext(base) {
	case ".go":
		return "text/x-go"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "text/javascript"
	case ".ts", ".tsx", ".mts", ".cts":
		return "text/typescript"
	case ".vue":
		return "text/x-vue"
	case ".py", ".pyw":
		return "text/x-python"
	case ".java":
		return "text/x-java-source"
	case ".c", ".h":
		return "text/x-c"
	case ".cc", ".cpp", ".cxx", ".hpp", ".hxx":
		return "text/x-c++"
	case ".cs":
		return "text/x-csharp"
	case ".rs":
		return "text/x-rust"
	case ".sh", ".bash", ".zsh", ".fish":
		return "text/x-shellscript"
	case ".sql":
		return "text/x-sql"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".scss":
		return "text/x-scss"
	case ".less":
		return "text/x-less"
	case ".json", ".jsonc":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".xml", ".svg":
		return "application/xml"
	case ".md", ".markdown":
		return "text/markdown"
	case ".php":
		return "text/x-php"
	case ".rb":
		return "text/x-ruby"
	case ".kt", ".kts":
		return "text/x-kotlin"
	case ".swift":
		return "text/x-swift"
	case ".dart":
		return "text/x-dart"
	case ".lua":
		return "text/x-lua"
	case ".scala":
		return "text/x-scala"
	default:
		return mime.TypeByExtension(path.Ext(base))
	}
}

func isInlineFileContentKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "source", "code", "content", "text":
		return true
	default:
		return false
	}
}

func isInlineFileDataKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(strings.TrimSpace(key)))
	return normalized == "filedata"
}

func inlineFileReference(object map[string]any) (string, string, bool) {
	for _, key := range []string{"path", "file_path", "filepath", "local_path", "source_path", "filename", "file_name"} {
		value, ok := object[key].(string)
		if !ok {
			continue
		}
		value = cleanFilePathCandidate(value)
		if looksLikeFilePath(value) {
			return value, stringValue(objectValue(object, "filename", "file_name", "name")), true
		}
	}
	kind := strings.ToLower(strings.TrimSpace(stringValue(object["type"])))
	if strings.Contains(kind, "file") {
		value := cleanFilePathCandidate(stringValue(object["name"]))
		if looksLikeFilePath(value) {
			return value, value, true
		}
	}
	return "", "", false
}

func inlineFileContent(object map[string]any, reference string) ([]byte, string, bool) {
	for _, key := range []string{"source", "code", "content", "text"} {
		content, ok := object[key].(string)
		if !ok || strings.TrimSpace(content) == "" || strings.TrimSpace(content) == reference {
			continue
		}
		return []byte(content), "", true
	}
	for _, key := range []string{"file_data", "fileData"} {
		encoded, ok := object[key].(string)
		if !ok || strings.TrimSpace(encoded) == "" {
			continue
		}
		decoded, mimeType, err := decodeInlineFileData(encoded)
		if err == nil && len(decoded) > 0 {
			return decoded, mimeType, true
		}
	}
	return nil, "", false
}

func decodeInlineFileData(value string) ([]byte, string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(value), "data:") {
		decoded, err := decodeResourceBase64(value)
		return decoded, "", err
	}
	comma := strings.IndexByte(value, ',')
	if comma <= 5 {
		return nil, "", errors.New("invalid file data URI")
	}
	header, encoded := value[5:comma], value[comma+1:]
	parts := strings.Split(header, ";")
	mimeType := strings.TrimSpace(parts[0])
	isBase64 := false
	for _, part := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			isBase64 = true
			break
		}
	}
	if isBase64 {
		decoded, err := decodeResourceBase64(encoded)
		return decoded, mimeType, err
	}
	decoded, err := url.PathUnescape(encoded)
	return []byte(decoded), mimeType, err
}

func addDetectedInlineFile(object map[string]any, location, role string, result *[]detectedResource) bool {
	reference, suppliedName, ok := inlineFileReference(object)
	if !ok {
		return false
	}
	content, dataMIMEType, ok := inlineFileContent(object, reference)
	if !ok {
		return false
	}

	suppliedMIMEType := stringValue(objectValue(object, "mime_type", "media_type", "mimeType"))
	mimeType := suppliedMIMEType
	if mimeType == "" {
		mimeType = dataMIMEType
	}
	if mimeType == "" {
		mimeType = sourceMIMETypeForPath(reference)
	}
	if mimeType == "" && utf8.Valid(content) {
		mimeType = "text/plain"
	}

	resource := detectedResource{Location: location + ".file", Role: role}
	resource.Kind = "file"
	resource.Name = suppliedName
	if resource.Name == "" {
		resource.Name = resourcePathBase(reference)
	}
	resource.MIMEType = mimeType
	resource.URI = truncateRunes(reference, 2048)
	resource.SizeBytes = int64(len(content))
	contentHash := sha256.Sum256(content)
	resource.SHA256 = hex.EncodeToString(contentHash[:])
	if utf8.Valid(content) && !strings.ContainsRune(string(content), '\x00') {
		resource.Preview = truncateRunes(string(content), 512)
	}
	fingerprintInput := "file|" + reference + "|" + suppliedName + "|" + suppliedMIMEType
	fingerprint := sha256.Sum256([]byte(fingerprintInput))
	resource.Fingerprint = hex.EncodeToString(fingerprint[:])
	*result = append(*result, resource)
	return true
}

func extractCodeSnippets(value string, allowUnfenced bool) []detectedCodeSnippet {
	result := make([]detectedCodeSnippet, 0)
	for _, matches := range fencedCodePattern.FindAllStringSubmatch(value, -1) {
		if len(matches) < 3 {
			continue
		}
		content := strings.TrimSpace(matches[2])
		if content == "" {
			continue
		}
		language, mimeType := normalizeCodeLanguage(matches[1], content)
		result = append(result, detectedCodeSnippet{Content: content, Language: language, MIMEType: mimeType})
	}
	remaining := strings.TrimSpace(fencedCodePattern.ReplaceAllString(value, ""))
	if allowUnfenced {
		if language, mimeType, ok := inferCodeLanguage(remaining); ok {
			result = append(result, detectedCodeSnippet{Content: remaining, Language: language, MIMEType: mimeType})
		}
	}
	return result
}

func normalizeCodeLanguage(language, content string) (string, string) {
	language = strings.ToLower(strings.TrimSpace(language))
	switch language {
	case "go", "golang":
		return "Go", "text/x-go"
	case "js", "javascript", "jsx":
		return "JavaScript", "text/javascript"
	case "ts", "typescript", "tsx":
		return "TypeScript", "text/typescript"
	case "vue":
		return "Vue", "text/x-vue"
	case "py", "python":
		return "Python", "text/x-python"
	case "java":
		return "Java", "text/x-java-source"
	case "c", "h":
		return "C", "text/x-c"
	case "cpp", "c++", "cc", "hpp":
		return "C++", "text/x-c++"
	case "cs", "csharp":
		return "C#", "text/x-csharp"
	case "rs", "rust":
		return "Rust", "text/x-rust"
	case "sh", "shell", "bash", "zsh", "fish":
		return "Shell", "text/x-shellscript"
	case "sql":
		return "SQL", "text/x-sql"
	case "html":
		return "HTML", "text/html"
	case "css":
		return "CSS", "text/css"
	case "scss":
		return "SCSS", "text/x-scss"
	case "less":
		return "Less", "text/x-less"
	case "json", "jsonc":
		return "JSON", "application/json"
	case "yaml", "yml":
		return "YAML", "application/yaml"
	case "xml":
		return "XML", "application/xml"
	case "diff", "patch":
		return "Diff", "text/x-diff"
	case "md", "markdown":
		return "Markdown", "text/markdown"
	case "php":
		return "PHP", "text/x-php"
	case "rb", "ruby":
		return "Ruby", "text/x-ruby"
	case "kt", "kts", "kotlin":
		return "Kotlin", "text/x-kotlin"
	case "swift":
		return "Swift", "text/x-swift"
	case "dart":
		return "Dart", "text/x-dart"
	case "lua":
		return "Lua", "text/x-lua"
	case "scala":
		return "Scala", "text/x-scala"
	case "dockerfile":
		return "Dockerfile", "text/x-dockerfile"
	case "":
		if inferred, mimeType, ok := inferCodeLanguage(content); ok {
			return inferred, mimeType
		}
		return "代码", "text/plain"
	default:
		return language, "text/plain"
	}
}

func inferCodeLanguage(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 40 || strings.Count(value, "\n") < 1 {
		return "", "", false
	}
	checks := []struct {
		language string
		mimeType string
		patterns []string
	}{
		{"Diff", "text/x-diff", []string{"diff --git ", "*** Begin Patch", "@@ -"}},
		{"Vue", "text/x-vue", []string{"<template", "<script"}},
		{"Go", "text/x-go", []string{"package ", "func ", "import ("}},
		{"Python", "text/x-python", []string{"def ", "class ", "if __name__"}},
		{"TypeScript", "text/typescript", []string{"interface ", "type ", "import ", "export ", "const ", "=>"}},
		{"JavaScript", "text/javascript", []string{"function ", "import ", "export ", "const ", "let ", "=>"}},
		{"Java", "text/x-java-source", []string{"public class ", "private class ", "package ", "import java."}},
		{"Rust", "text/x-rust", []string{"fn ", "impl ", "use std::", "pub struct "}},
		{"C/C++", "text/x-c++", []string{"#include ", "int main(", "namespace ", "std::"}},
		{"Shell", "text/x-shellscript", []string{"#!/bin/", "set -e", "function ", "case \"$"}},
		{"SQL", "text/x-sql", []string{"SELECT ", "INSERT INTO ", "CREATE TABLE ", "UPDATE "}},
		{"HTML", "text/html", []string{"<!DOCTYPE html", "<html", "<body", "</"}},
		{"CSS", "text/css", []string{"@media ", "display:", "color:", "{"}},
	}
	lowerValue := strings.ToLower(value)
	for _, check := range checks {
		matches := 0
		for _, pattern := range check.patterns {
			if strings.Contains(lowerValue, strings.ToLower(pattern)) {
				matches++
			}
		}
		if matches >= 2 {
			return check.language, check.mimeType, true
		}
	}
	return "", "", false
}

func addDetectedCodeResource(snippet detectedCodeSnippet, location, role string, result *[]detectedResource) {
	content := strings.TrimSpace(snippet.Content)
	if content == "" {
		return
	}
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	language := strings.TrimSpace(snippet.Language)
	if language == "" {
		language = "代码"
	}
	resource := detectedResource{Location: location, Role: role}
	resource.Kind = "code"
	resource.Name = "代码片段"
	if language != "代码" {
		resource.Name = language + "代码片段"
	}
	resource.MIMEType = snippet.MIMEType
	resource.URI = "code:" + hash
	resource.SizeBytes = int64(len([]byte(content)))
	resource.SHA256 = hash
	resource.Preview = truncateRunes(content, 512)
	fingerprint := sha256.Sum256([]byte("code|" + hash + "|" + snippet.MIMEType))
	resource.Fingerprint = hex.EncodeToString(fingerprint[:])
	*result = append(*result, resource)
}

func extractHTTPURLs(value string) []string {
	matches := httpURLPattern.FindAllString(value, -1)
	if len(matches) == 0 {
		return nil
	}
	urls := make([]string, 0, len(matches))
	for _, match := range matches {
		match = strings.TrimRight(match, "()[]{}<>.,;:")
		if match == "" {
			continue
		}
		if parsed, err := url.Parse(match); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			continue
		}
		urls = append(urls, match)
	}
	return urls
}

func addDetectedResource(value string, object map[string]any, location, role string, result *[]detectedResource) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if !strings.HasPrefix(value, "data:") {
		if urls := extractHTTPURLs(value); len(urls) > 0 {
			value = urls[0]
		}
	}
	resource := detectedResource{Location: location, Role: role}
	resource.Kind = "reference"
	if role == "image" {
		resource.Kind = "image"
	}
	resource.URI = truncateRunes(value, 2048)
	resource.Name = stringValue(objectValue(object, "filename", "file_name", "name"))
	resource.MIMEType = stringValue(objectValue(object, "mime_type", "media_type", "mimeType"))
	if strings.HasPrefix(value, "data:") {
		resource.Kind = "data"
		if comma := strings.IndexByte(value, ','); comma > 5 {
			header, encoded := value[5:comma], value[comma+1:]
			if semi := strings.IndexByte(header, ';'); semi >= 0 {
				if resource.MIMEType == "" {
					resource.MIMEType = header[:semi]
				}
			} else if resource.MIMEType == "" {
				resource.MIMEType = header
			}
			decoded, err := decodeResourceBase64(encoded)
			if err == nil {
				resource.SizeBytes = int64(len(decoded))
				sum := sha256.Sum256(decoded)
				resource.SHA256 = hex.EncodeToString(sum[:])
				// Keep the bounded URI field suitable for labels/search, but retain the
				// complete browser-previewable data URI separately so the browser receives
				// all bytes instead of the truncated label value.
				if isBrowserPreviewMIME(resource.MIMEType) && len(value) <= 2<<20 {
					resource.PreviewData = value
					if strings.HasPrefix(strings.ToLower(resource.MIMEType), "image/") {
						resource.Kind = "image"
					}
				}
			}
		}
	} else if parsed, err := url.Parse(value); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		if resource.Kind != "image" {
			resource.Kind = "url"
		}
		if resource.Name == "" {
			resource.Name = path.Base(parsed.Path)
		}
	}
	if resource.Kind == "reference" && resource.SHA256 == "" {
		return
	}
	fingerprintInput := resource.SHA256
	if fingerprintInput == "" {
		fingerprintInput = resource.Kind + "|" + value + "|" + resource.Name + "|" + resource.MIMEType
	}
	sum := sha256.Sum256([]byte(fingerprintInput))
	resource.Fingerprint = hex.EncodeToString(sum[:])
	if resource.Preview == "" {
		resource.Preview = truncateRunes(value, 240)
	}
	*result = append(*result, resource)
}

func isBrowserPreviewMIME(value string) bool {
	mime := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	return strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "audio/") || strings.HasPrefix(mime, "video/") || mime == "application/pdf" || strings.HasPrefix(mime, "text/") || mime == "application/json" || mime == "application/xml" || mime == "application/javascript"
}

func decodeResourceBase64(value string) ([]byte, error) {
	value = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		default:
			return r
		}
	}, strings.TrimSpace(value))
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	for _, encoding := range encodings {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64 resource data")
}

func objectValue(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			return value
		}
	}
	return nil
}

func persistRequestResources(db *gorm.DB, requestID string, tokenID uint64, sessionID string, body []byte, now time.Time) error {
	detected := detectResources(body)
	if len(detected) == 0 {
		return nil
	}
	for index, item := range detected {
		var resource GatewayResource
		if err := db.Where("fingerprint = ?", item.Fingerprint).First(&resource).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			item.FirstSeenAt, item.LastSeenAt = now, now
			if err := db.Create(&item.GatewayResource).Error; err != nil {
				return err
			}
			resource = item.GatewayResource
		} else {
			updates := map[string]any{"last_seen_at": now}
			contentChanged := item.SHA256 != "" && item.SHA256 != resource.SHA256
			if contentChanged {
				updates["mime_type"] = item.MIMEType
				updates["size_bytes"] = item.SizeBytes
				updates["sha256"] = item.SHA256
				updates["preview"] = item.Preview
			}
			if resource.Name == "" && item.Name != "" {
				updates["name"] = item.Name
			}
			if !contentChanged && resource.MIMEType == "" && item.MIMEType != "" {
				updates["mime_type"] = item.MIMEType
			}
			if !contentChanged && resource.SizeBytes == 0 && item.SizeBytes > 0 {
				updates["size_bytes"] = item.SizeBytes
			}
			if !contentChanged && resource.SHA256 == "" && item.SHA256 != "" {
				updates["sha256"] = item.SHA256
			}
			if !contentChanged && item.Preview != "" && item.Preview != item.URI && (resource.Preview == "" || resource.Preview == resource.URI || len(item.Preview) > len(resource.Preview)) {
				updates["preview"] = item.Preview
			}
			if err := db.Model(&resource).Updates(updates).Error; err != nil {
				return err
			}
		}
		association := RequestResource{RequestID: requestID, ResourceID: resource.ID, Location: truncateRunes(item.Location, 512), Role: truncateRunes(item.Role, 32), Occurrence: 1}
		if tokenID > 0 && strings.TrimSpace(sessionID) != "" {
			var existing RequestResource
			err := db.Table("request_resources rr").Joins("JOIN relay_request_logs l ON l.id = rr.request_id").Where("rr.resource_id = ? AND l.token_id = ? AND l.codex_session_id = ?", resource.ID, tokenID, sessionID).First(&existing).Error
			if err == nil {
				continue
			}
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if err := db.Where("request_id = ? AND resource_id = ? AND location = ?", association.RequestID, association.ResourceID, association.Location).FirstOrCreate(&association).Error; err != nil {
			return err
		}
		_ = index
	}
	return nil
}

func loadRequestResources(db *gorm.DB, requestID string) ([]ResourceView, error) {
	items, err := loadRequestResourcesBatch(db, []string{requestID}, true)
	if err != nil {
		return nil, err
	}
	return items[requestID], nil
}

func loadRequestResourcesBatch(db *gorm.DB, requestIDs []string, includePreviews bool) (map[string][]ResourceView, error) {
	result := make(map[string][]ResourceView, len(requestIDs))
	if len(requestIDs) == 0 {
		return result, nil
	}
	for _, requestID := range requestIDs {
		result[requestID] = make([]ResourceView, 0)
	}
	var rows []struct {
		GatewayResource
		RequestID string
		Location  string
	}
	if err := db.Table("gateway_resources AS r").Select("r.*, rr.request_id, rr.location").Joins("JOIN request_resources rr ON rr.resource_id = r.id").Where("rr.request_id IN ?", requestIDs).Where("r.kind <> ? OR r.sha256 <> ''", "file").Order("rr.id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	indexes := make(map[string]map[uint64]int, len(requestIDs))
	missing := make(map[uint64]string)
	targets := make(map[string][]struct {
		RequestID string
		Index     int
	})
	for _, row := range rows {
		if indexes[row.RequestID] == nil {
			indexes[row.RequestID] = make(map[uint64]int)
		}
		pos, ok := indexes[row.RequestID][row.ID]
		if !ok {
			pos = len(result[row.RequestID])
			indexes[row.RequestID][row.ID] = pos
			result[row.RequestID] = append(result[row.RequestID], ResourceView{GatewayResource: row.GatewayResource})
			if row.PreviewData == "" {
				missing[row.ID] = row.Fingerprint
				targets[row.Fingerprint] = append(targets[row.Fingerprint], struct {
					RequestID string
					Index     int
				}{RequestID: row.RequestID, Index: pos})
			}
		}
		result[row.RequestID][pos].Locations = append(result[row.RequestID][pos].Locations, row.Location)
	}
	if len(missing) == 0 {
		return result, nil
	}
	if !includePreviews {
		return result, nil
	}
	bodyIDs := make([]string, 0, len(requestIDs))
	for _, requestID := range requestIDs {
		if _, ok := result[requestID]; ok {
			bodyIDs = append(bodyIDs, requestID)
		}
	}
	type requestBodyRow struct {
		ID          string
		RequestBody string
	}
	var bodies []requestBodyRow
	if err := db.Model(&RelayRequestLog{}).Select("id, request_body").Where("id IN ?", bodyIDs).Find(&bodies).Error; err != nil {
		return nil, err
	}
	positions := make(map[string]int, len(bodies))
	for index := range bodies {
		positions[bodies[index].ID] = index
	}
	var payloads []RelayRequestPayload
	if err := db.Select("request_id, request_body").Where("request_id IN ?", bodyIDs).Find(&payloads).Error; err != nil {
		return nil, err
	}
	for _, payload := range payloads {
		if index, ok := positions[payload.RequestID]; ok {
			bodies[index].RequestBody = payload.RequestBody
		}
	}
	for _, body := range bodies {
		if body.RequestBody == "" {
			continue
		}
		for _, detected := range detectResources([]byte(decompressStoredPayload(body.RequestBody))) {
			for _, target := range targets[detected.Fingerprint] {
				item := &result[target.RequestID][target.Index]
				if item.PreviewData == "" && detected.PreviewData != "" {
					item.PreviewData = detected.PreviewData
					item.Kind = "image"
				}
			}
		}
	}
	return result, nil
}

func resourcePreviewFromBody(storedBody, fingerprint string) string {
	if storedBody == "" || fingerprint == "" {
		return ""
	}
	for _, item := range detectResources([]byte(decompressStoredPayload(storedBody))) {
		if item.Fingerprint == fingerprint && item.PreviewData != "" {
			return item.PreviewData
		}
	}
	return ""
}

func loadSessionResources(db *gorm.DB, tokenID uint64, sessionID, requestID string, identified, includePreviews bool) ([]ResourceSummary, error) {
	query := db.Table("request_resources rr").Select("r.*, COUNT(DISTINCT rr.request_id) AS request_count, COUNT(*) AS use_count").Joins("JOIN gateway_resources r ON r.id = rr.resource_id").Joins("JOIN relay_request_logs l ON l.id = rr.request_id").Where("r.kind <> ? OR r.sha256 <> ''", "file")
	if identified {
		query = query.Where("l.token_id = ? AND l.codex_session_id = ?", tokenID, sessionID)
	} else {
		query = query.Where("l.id = ? AND l.codex_session_id = ''", requestID)
	}
	var items []ResourceSummary
	if err := query.Group("r.id").Order("use_count DESC, r.last_seen_at DESC").Scan(&items).Error; err != nil {
		return nil, err
	}
	if !includePreviews {
		return items, nil
	}
	missing := make(map[uint64]string)
	for index := range items {
		if items[index].PreviewData == "" {
			missing[items[index].ID] = items[index].Fingerprint
		}
	}
	requestIDs, err := previewRequestIDs(db, missing, 20)
	if err != nil {
		return nil, err
	}
	previews, err := loadResourcePreviews(db, missing, requestIDs)
	if err != nil {
		return nil, err
	}
	for index := range items {
		if preview := previews[items[index].ID]; items[index].PreviewData == "" && preview != "" {
			items[index].PreviewData = preview
			items[index].Kind = "image"
		}
	}
	return items, nil
}

func resourcePreviewForResource(db *gorm.DB, resourceID uint64, fingerprint string) string {
	missing := map[uint64]string{resourceID: fingerprint}
	requestIDs, err := previewRequestIDs(db, missing, 20)
	if err != nil {
		return ""
	}
	previews, err := loadResourcePreviews(db, missing, requestIDs)
	if err != nil {
		return ""
	}
	return previews[resourceID]
}

func previewRequestIDs(db *gorm.DB, resources map[uint64]string, perResourceLimit int) ([]string, error) {
	if len(resources) == 0 {
		return nil, nil
	}
	resourceIDs := make([]uint64, 0, len(resources))
	for resourceID := range resources {
		resourceIDs = append(resourceIDs, resourceID)
	}
	type association struct {
		ResourceID uint64
		RequestID  string
	}
	var associations []association
	if err := db.Model(&RequestResource{}).Select("resource_id, request_id").Where("resource_id IN ?", resourceIDs).Order("id ASC").Find(&associations).Error; err != nil {
		return nil, err
	}
	counts := make(map[uint64]int, len(resources))
	seen := make(map[string]struct{})
	requestIDs := make([]string, 0)
	for _, association := range associations {
		if perResourceLimit > 0 && counts[association.ResourceID] >= perResourceLimit {
			continue
		}
		counts[association.ResourceID]++
		if _, ok := seen[association.RequestID]; ok {
			continue
		}
		seen[association.RequestID] = struct{}{}
		requestIDs = append(requestIDs, association.RequestID)
	}
	return requestIDs, nil
}

func loadResourcePreviews(db *gorm.DB, resources map[uint64]string, requestIDs []string) (map[uint64]string, error) {
	previews := make(map[uint64]string)
	if len(resources) == 0 || len(requestIDs) == 0 {
		return previews, nil
	}
	type requestBodyRow struct {
		ID          string
		RequestBody string
	}
	var bodies []requestBodyRow
	if err := db.Model(&RelayRequestLog{}).Select("id, request_body").Where("id IN ?", requestIDs).Find(&bodies).Error; err != nil {
		return nil, err
	}
	positions := make(map[string]int, len(bodies))
	for index := range bodies {
		positions[bodies[index].ID] = index
	}
	var payloads []RelayRequestPayload
	if err := db.Select("request_id, request_body").Where("request_id IN ?", requestIDs).Find(&payloads).Error; err != nil {
		return nil, err
	}
	for _, payload := range payloads {
		if index, ok := positions[payload.RequestID]; ok {
			bodies[index].RequestBody = payload.RequestBody
		}
	}
	resourceByFingerprint := make(map[string]uint64, len(resources))
	for resourceID, fingerprint := range resources {
		resourceByFingerprint[fingerprint] = resourceID
	}
	for _, body := range bodies {
		if body.RequestBody == "" {
			continue
		}
		for _, detected := range detectResources([]byte(decompressStoredPayload(body.RequestBody))) {
			resourceID, ok := resourceByFingerprint[detected.Fingerprint]
			if !ok || detected.PreviewData == "" || previews[resourceID] != "" {
				continue
			}
			previews[resourceID] = detected.PreviewData
		}
		if len(previews) == len(resources) {
			break
		}
	}
	return previews, nil
}

func (s *ManagementService) Resources(ctx context.Context, query ResourceQuery) (*ResourcePage, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 || query.PageSize > 200 {
		query.PageSize = 50
	}
	db := s.store.db.WithContext(ctx).Model(&GatewayResource{}).Where("kind <> ? OR sha256 <> ''", "file")
	if value := strings.TrimSpace(query.Search); value != "" {
		pattern := "%" + value + "%"
		db = db.Where("name LIKE ? OR uri LIKE ? OR preview LIKE ? OR sha256 LIKE ?", pattern, pattern, pattern, pattern)
	}
	if value := strings.TrimSpace(query.Kind); value != "" {
		db = db.Where("kind = ?", value)
	}
	if query.Sensitive != nil {
		db = db.Where("sensitive = ?", *query.Sensitive)
	}
	if query.LastSeenBefore != nil {
		db = db.Where("last_seen_at < ?", query.LastSeenBefore.UTC())
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, err
	}
	var resources []GatewayResource
	if err := db.Order("sensitive DESC, last_seen_at DESC, id DESC").Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Find(&resources).Error; err != nil {
		return nil, err
	}
	items := make([]ResourceCatalogItem, 0, len(resources))
	for _, resource := range resources {
		if resource.PreviewData == "" {
			resource.PreviewData = resourcePreviewForResource(s.store.db.WithContext(ctx), resource.ID, resource.Fingerprint)
			if resource.PreviewData != "" {
				resource.Kind = "image"
			}
		}
		var row struct {
			RequestCount int64
			UseCount     int64
			SessionCount int64
		}
		if err := s.store.db.WithContext(ctx).Table("request_resources rr").Select("COUNT(DISTINCT rr.request_id) AS request_count, COALESCE(SUM(rr.occurrence), COUNT(*)) AS use_count, COUNT(DISTINCT CASE WHEN l.codex_session_id <> '' THEN printf('%d:%s', l.token_id, l.codex_session_id) ELSE l.id END) AS session_count").Joins("JOIN relay_request_logs l ON l.id = rr.request_id").Where("rr.resource_id = ?", resource.ID).Scan(&row).Error; err != nil {
			return nil, err
		}
		items = append(items, ResourceCatalogItem{ResourceSummary: ResourceSummary{GatewayResource: resource, RequestCount: row.RequestCount, UseCount: row.UseCount}, SessionCount: row.SessionCount})
	}
	return &ResourcePage{Items: items, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *ManagementService) ResourceDetail(ctx context.Context, id uint64) (*ResourceDetail, error) {
	var resource GatewayResource
	if err := s.store.db.WithContext(ctx).First(&resource, id).Error; err != nil {
		return nil, err
	}
	if resource.Kind == "file" && resource.SHA256 == "" {
		return nil, gorm.ErrRecordNotFound
	}
	if resource.PreviewData == "" {
		resource.PreviewData = resourcePreviewForResource(s.store.db.WithContext(ctx), resource.ID, resource.Fingerprint)
		if resource.PreviewData != "" {
			resource.Kind = "image"
		}
	}
	var sessions []ResourceSessionRef
	if err := s.store.db.WithContext(ctx).Table("request_resources rr").Select("l.token_id, l.codex_session_id AS session_id, CASE WHEN l.codex_session_id = '' THEN l.id ELSE '' END AS fallback_request_id, COALESCE(NULLIF(MAX(l.session_name), ''), NULLIF(l.codex_session_id, ''), l.id) AS session_name, COUNT(DISTINCT l.id) AS request_count").Joins("JOIN relay_request_logs l ON l.id = rr.request_id").Where("rr.resource_id = ?", id).Group("l.token_id, l.codex_session_id, CASE WHEN l.codex_session_id = '' THEN l.id ELSE l.codex_session_id END").Order("MAX(l.created_at) DESC").Scan(&sessions).Error; err != nil {
		return nil, err
	}
	var requests []ResourceRequestRef
	if err := s.store.db.WithContext(ctx).Table("request_resources rr").Select("l.id, l.codex_session_id AS session_id, l.session_name, l.requested_model, l.endpoint, l.created_at, rr.location").Joins("JOIN relay_request_logs l ON l.id = rr.request_id").Where("rr.resource_id = ?", id).Order("l.created_at DESC, l.id DESC").Scan(&requests).Error; err != nil {
		return nil, err
	}
	return &ResourceDetail{Resource: resource, Sessions: sessions, Requests: requests}, nil
}

func (s *ManagementService) SetResourceSensitive(ctx context.Context, id uint64, sensitive bool) error {
	result := s.store.db.WithContext(ctx).Model(&GatewayResource{}).Where("id = ?", id).Update("sensitive", sensitive)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func hasSensitiveResources(db *gorm.DB, requestID string) (bool, error) {
	var count int64
	err := db.Table("request_resources rr").Joins("JOIN gateway_resources r ON r.id = rr.resource_id").Where("rr.request_id = ? AND r.sensitive = ?", requestID, true).Where("r.kind <> ? OR r.sha256 <> ''", "file").Count(&count).Error
	return count > 0, err
}

func hasSensitiveSessionResources(db *gorm.DB, tokenID uint64, sessionID, requestID string, identified bool) (bool, error) {
	query := db.Table("request_resources rr").Joins("JOIN gateway_resources r ON r.id = rr.resource_id").Joins("JOIN relay_request_logs l ON l.id = rr.request_id").Where("r.sensitive = ?", true).Where("r.kind <> ? OR r.sha256 <> ''", "file")
	if identified {
		query = query.Where("l.token_id = ? AND l.codex_session_id = ?", tokenID, sessionID)
	} else {
		query = query.Where("l.id = ? AND l.codex_session_id = ''", requestID)
	}
	var count int64
	err := query.Count(&count).Error
	return count > 0, err
}
