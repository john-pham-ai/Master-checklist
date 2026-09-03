package confluence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"time"
)

// Client talks to the Confluence Cloud v2 REST API using bot HTTP Basic Auth.
//
// v2 is used (rather than v1) because ParentPageID may point at either a
// regular page (e.g. Master Testing) or a folder (e.g. Candidate Testing) —
// v1's /rest/api/content only understands page ancestors, but v2's
// parentId is generic across pages and folders.
type Client struct {
	BaseURL      string // e.g. https://appliedintuition.atlassian.net/wiki
	SpaceKey     string // e.g. NEURON
	ParentPageID string // page or folder ID to file runs under
	BotEmail     string
	APIToken     string

	httpClient *http.Client
	spaceID    string
}

func NewClient(baseURL, spaceKey, parentPageID, botEmail, apiToken string) *Client {
	return &Client{
		BaseURL:      baseURL,
		SpaceKey:     spaceKey,
		ParentPageID: parentPageID,
		BotEmail:     botEmail,
		APIToken:     apiToken,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) do(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.BotEmail, c.APIToken)
	req.Header.Set("Content-Type", "application/json")
	return c.httpClient.Do(req)
}

type spaceListResult struct {
	Results []struct {
		ID string `json:"id"`
	} `json:"results"`
}

// spaceID resolves and caches the numeric space ID for c.SpaceKey, which v2
// endpoints require in place of the space key.
func (c *Client) getSpaceID() (string, error) {
	if c.spaceID != "" {
		return c.spaceID, nil
	}

	resp, err := c.do("GET", "/api/v2/spaces?keys="+url.QueryEscape(c.SpaceKey), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("list spaces failed: %s: %s", resp.Status, b)
	}

	var result spaceListResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Results) == 0 {
		return "", fmt.Errorf("no space found for key %q", c.SpaceKey)
	}

	c.spaceID = result.Results[0].ID
	return c.spaceID, nil
}

type pageListResult struct {
	Results []struct {
		ID       string `json:"id"`
		ParentID string `json:"parentId"`
	} `json:"results"`
}

type createPageRequest struct {
	SpaceID  string `json:"spaceId"`
	Status   string `json:"status"`
	Title    string `json:"title"`
	ParentID string `json:"parentId"`
	Body     struct {
		Representation string `json:"representation"`
		Value          string `json:"value"`
	} `json:"body"`
}

type pageResult struct {
	ID    string `json:"id"`
	Links struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
}

// FindOrCreateMonthPage returns the ID of the child page under ParentPageID titled
// monthTitle (e.g. "September 2026"), creating it if it doesn't exist yet.
// ParentPageID may be a page or a folder.
func (c *Client) FindOrCreateMonthPage(monthTitle string) (string, error) {
	spaceID, err := c.getSpaceID()
	if err != nil {
		return "", err
	}

	path := fmt.Sprintf("/api/v2/pages?space-id=%s&title=%s&status=current", url.QueryEscape(spaceID), url.QueryEscape(monthTitle))
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("list pages failed: %s: %s", resp.Status, b)
	}
	var results pageListResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return "", err
	}
	for _, page := range results.Results {
		if page.ParentID == c.ParentPageID {
			return page.ID, nil
		}
	}

	created, err := c.createPage(spaceID, monthTitle, c.ParentPageID, fmt.Sprintf("<p>Smoke test runs for %s.</p>", monthTitle))
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// CreateRunPage creates the run's checklist page as a child of parentID.
// Returns the new page's ID (needed to attach screenshots/clips afterward)
// and its absolute URL.
func (c *Client) CreateRunPage(parentID, title, storageBody string) (pageID, pageURL string, err error) {
	spaceID, err := c.getSpaceID()
	if err != nil {
		return "", "", err
	}

	created, err := c.createPage(spaceID, title, parentID, storageBody)
	if err != nil {
		return "", "", err
	}
	return created.ID, c.BaseURL + created.Links.WebUI, nil
}

// UploadAttachment uploads one file as an attachment on an existing page.
// filename must match whatever ri:attachment reference was baked into the
// page body (see mediaCell) for the macro to resolve to this file.
//
// This uses the v1 content API (rather than v2) because, as of this writing,
// v2 has no attachment-upload endpoint; v1's /rest/api/content accepts the
// same numeric page IDs v2 hands back, so mixing them here is safe.
func (c *Client) UploadAttachment(pageID, filename, contentType string, data io.Reader) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest("POST", c.BaseURL+"/rest/api/content/"+url.PathEscape(pageID)+"/child/attachment", body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.BotEmail, c.APIToken)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	// Required by Confluence to allow file uploads without a form-submitted token.
	req.Header.Set("X-Atlassian-Token", "nocheck")

	// Screen/webcam clips can run tens of MB; the shared httpClient's 15s
	// timeout (sized for small JSON calls) is too tight for that upload.
	uploadClient := &http.Client{Timeout: 3 * time.Minute}
	resp, err := uploadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload attachment %q failed: %s: %s", filename, resp.Status, b)
	}
	return nil
}

func (c *Client) createPage(spaceID, title, parentID, storageBody string) (*pageResult, error) {
	req := createPageRequest{
		SpaceID:  spaceID,
		Status:   "current",
		Title:    title,
		ParentID: parentID,
	}
	req.Body.Representation = "storage"
	req.Body.Value = storageBody

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.do("POST", "/api/v2/pages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create page failed: %s: %s", resp.Status, b)
	}
	var result pageResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
