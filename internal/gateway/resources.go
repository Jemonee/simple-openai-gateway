package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

type GatewayResource struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Fingerprint string    `gorm:"size:64;uniqueIndex;not null" json:"fingerprint"`
	Kind        string    `gorm:"size:24;index;not null" json:"kind"`
	Name        string    `gorm:"size:255" json:"name"`
	MIMEType    string    `gorm:"size:120" json:"mimeType"`
	URI         string    `gorm:"size:2048" json:"uri"`
	SizeBytes   int64     `gorm:"not null;default:0" json:"sizeBytes"`
	SHA256      string    `gorm:"size:64" json:"sha256"`
	Preview     string    `gorm:"size:512" json:"preview"`
	PreviewData string    `gorm:"type:text" json:"previewData"`
	Sensitive   bool      `gorm:"not null;default:false;index" json:"sensitive"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
}

type RequestResource struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	RequestID  string `gorm:"size:36;index;not null" json:"requestId"`
	ResourceID uint64 `gorm:"index;not null" json:"resourceId"`
	Location   string `gorm:"size:512;not null" json:"location"`
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
	Search    string
	Kind      string
	Sensitive *bool
	Page      int
	PageSize  int
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
	TokenID      uint64 `json:"tokenId"`
	SessionID    string `json:"sessionId"`
	SessionName  string `json:"sessionName"`
	RequestCount int64  `json:"requestCount"`
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

func detectResources(body []byte) []detectedResource {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil
	}
	result := make([]detectedResource, 0)
	walkResourceValue(value, "$", "request", &result)
	return result
}

func walkResourceValue(value any, location, role string, result *[]detectedResource) {
	switch typed := value.(type) {
	case []any:
		for index, item := range typed {
			walkResourceValue(item, fmt.Sprintf("%s[%d]", location, index), role, result)
		}
	case map[string]any:
		currentRole := role
		if kind := strings.ToLower(strings.TrimSpace(stringValue(typed["type"]))); strings.Contains(kind, "image") {
			currentRole = "image"
		}
		for _, key := range []string{"url", "image_url", "file_url", "uri", "file_id", "fileId"} {
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
			if key == "url" || key == "image_url" || key == "file_url" || key == "uri" || key == "file_id" || key == "fileId" {
				continue
			}
			itemRole := currentRole
			if key == "image_url" {
				itemRole = "image"
			}
			walkResourceValue(item, fmt.Sprintf("%s.%s", location, key), itemRole, result)
		}
	case string:
		for _, token := range extractHTTPURLs(typed) {
			addDetectedResource(token, nil, location, role, result)
		}
	}
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
	} else if strings.HasPrefix(value, "file-") || strings.HasPrefix(value, "file_") {
		resource.Kind = "file"
	} else if parsed, err := url.Parse(value); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		if resource.Kind != "image" {
			resource.Kind = "url"
		}
		if resource.Name == "" {
			resource.Name = path.Base(parsed.Path)
		}
	} else if !strings.Contains(value, " ") && (strings.HasSuffix(strings.ToLower(value), ".pdf") || strings.Contains(value, "/")) {
		resource.Kind = "file"
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
	resource.Preview = truncateRunes(value, 240)
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
		} else if err := db.Model(&resource).Updates(map[string]any{"last_seen_at": now}).Error; err != nil {
			return err
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
	var rows []struct {
		GatewayResource
		Location    string
		RequestBody string
	}
	if err := db.Table("gateway_resources AS r").Select("r.*, rr.location, l.request_body").Joins("JOIN request_resources rr ON rr.resource_id = r.id").Joins("JOIN relay_request_logs l ON l.id = rr.request_id").Where("rr.request_id = ?", requestID).Order("rr.id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]ResourceView, 0, len(rows))
	index := map[uint64]int{}
	for _, row := range rows {
		pos, ok := index[row.ID]
		if !ok {
			pos = len(items)
			index[row.ID] = pos
			items = append(items, ResourceView{GatewayResource: row.GatewayResource})
		}
		if items[pos].PreviewData == "" {
			items[pos].PreviewData = resourcePreviewFromBody(row.RequestBody, items[pos].Fingerprint)
			if items[pos].PreviewData != "" {
				items[pos].Kind = "image"
			}
		}
		items[pos].Locations = append(items[pos].Locations, row.Location)
	}
	return items, nil
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

func loadSessionResources(db *gorm.DB, tokenID uint64, sessionID, requestID string, identified bool) ([]ResourceSummary, error) {
	query := db.Table("request_resources rr").Select("r.*, COUNT(DISTINCT rr.request_id) AS request_count, COUNT(*) AS use_count").Joins("JOIN gateway_resources r ON r.id = rr.resource_id").Joins("JOIN relay_request_logs l ON l.id = rr.request_id")
	if identified {
		query = query.Where("l.token_id = ? AND l.codex_session_id = ?", tokenID, sessionID)
	} else {
		query = query.Where("l.id = ? AND l.codex_session_id = ''", requestID)
	}
	var items []ResourceSummary
	if err := query.Group("r.id").Order("use_count DESC, r.last_seen_at DESC").Scan(&items).Error; err != nil {
		return nil, err
	}
	for index := range items {
		items[index].PreviewData = resourcePreviewForResource(db, items[index].ID, items[index].Fingerprint)
		if items[index].PreviewData != "" {
			items[index].Kind = "image"
		}
	}
	return items, nil
}

func resourcePreviewForResource(db *gorm.DB, resourceID uint64, fingerprint string) string {
	var bodies []string
	if err := db.Table("request_resources rr").Select("l.request_body").Joins("JOIN relay_request_logs l ON l.id = rr.request_id").Where("rr.resource_id = ? AND l.request_body <> ''", resourceID).Order("rr.id ASC").Limit(20).Scan(&bodies).Error; err != nil {
		return ""
	}
	for _, body := range bodies {
		if preview := resourcePreviewFromBody(body, fingerprint); preview != "" {
			return preview
		}
	}
	return ""
}

func (s *ManagementService) Resources(ctx context.Context, query ResourceQuery) (*ResourcePage, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 || query.PageSize > 200 {
		query.PageSize = 50
	}
	db := s.store.db.WithContext(ctx).Model(&GatewayResource{})
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
	if resource.PreviewData == "" {
		resource.PreviewData = resourcePreviewForResource(s.store.db.WithContext(ctx), resource.ID, resource.Fingerprint)
		if resource.PreviewData != "" {
			resource.Kind = "image"
		}
	}
	var sessions []ResourceSessionRef
	if err := s.store.db.WithContext(ctx).Table("request_resources rr").Select("l.token_id, l.codex_session_id AS session_id, COALESCE(NULLIF(MAX(l.session_name), ''), l.codex_session_id, l.id) AS session_name, COUNT(DISTINCT l.id) AS request_count").Joins("JOIN relay_request_logs l ON l.id = rr.request_id").Where("rr.resource_id = ?", id).Group("l.token_id, l.codex_session_id, CASE WHEN l.codex_session_id = '' THEN l.id ELSE l.codex_session_id END").Order("MAX(l.created_at) DESC").Scan(&sessions).Error; err != nil {
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
	err := db.Table("request_resources rr").Joins("JOIN gateway_resources r ON r.id = rr.resource_id").Where("rr.request_id = ? AND r.sensitive = ?", requestID, true).Count(&count).Error
	return count > 0, err
}

func hasSensitiveSessionResources(db *gorm.DB, tokenID uint64, sessionID, requestID string, identified bool) (bool, error) {
	query := db.Table("request_resources rr").Joins("JOIN gateway_resources r ON r.id = rr.resource_id").Joins("JOIN relay_request_logs l ON l.id = rr.request_id").Where("r.sensitive = ?", true)
	if identified {
		query = query.Where("l.token_id = ? AND l.codex_session_id = ?", tokenID, sessionID)
	} else {
		query = query.Where("l.id = ? AND l.codex_session_id = ''", requestID)
	}
	var count int64
	err := query.Count(&count).Error
	return count > 0, err
}
