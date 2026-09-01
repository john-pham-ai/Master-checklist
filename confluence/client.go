package confluence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to the Confluence Cloud REST API using bot HTTP Basic Auth.
type Client struct {
	BaseURL      string // e.g. https://appliedintuition.atlassian.net/wiki
	SpaceKey     string // e.g. NEURON
	ParentPageID string // e.g. 2693234852 (Master Testing)
	BotEmail     string
	APIToken     string

	httpClient *http.Client
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

type contentBody struct {
	Storage struct {
		Value          string `json:"value"`
		Representation string `json:"representation"`
	} `json:"storage"`
}

type createContentRequest struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Space  struct{ Key string `json:"key"` } `json:"space"`
	Ancestors []struct {
		ID string `json:"id"`
	} `json:"ancestors"`
	Body contentBody `json:"body"`
}

type contentResult struct {
	ID    string `json:"id"`
	Links struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
}

type childPageResults struct {
	Results []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"results"`
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

// FindOrCreateMonthPage returns the ID of the child page under ParentPageID titled
// monthTitle (e.g. "September 2026"), creating it if it doesn't exist yet.
func (c *Client) FindOrCreateMonthPage(monthTitle string) (string, error) {
	resp, err := c.do("GET", fmt.Sprintf("/rest/api/content/%s/child/page?limit=100", c.ParentPageID), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("list child pages failed: %s: %s", resp.Status, b)
	}
	var results childPageResults
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return "", err
	}
	for _, page := range results.Results {
		if page.Title == monthTitle {
			return page.ID, nil
		}
	}

	req := createContentRequest{Type: "page", Title: monthTitle}
	req.Space.Key = c.SpaceKey
	req.Ancestors = []struct{ ID string `json:"id"` }{{ID: c.ParentPageID}}
	req.Body.Storage.Value = fmt.Sprintf("<p>Smoke test runs for %s.</p>", monthTitle)
	req.Body.Storage.Representation = "storage"

	created, err := c.createContent(req)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// CreateRunPage creates the run's checklist page as a child of parentID.
// Returns the absolute URL of the created page.
func (c *Client) CreateRunPage(parentID, title, storageBody string) (string, error) {
	req := createContentRequest{Type: "page", Title: title}
	req.Space.Key = c.SpaceKey
	req.Ancestors = []struct{ ID string `json:"id"` }{{ID: parentID}}
	req.Body.Storage.Value = storageBody
	req.Body.Storage.Representation = "storage"

	created, err := c.createContent(req)
	if err != nil {
		return "", err
	}
	return c.BaseURL + created.Links.WebUI, nil
}

func (c *Client) createContent(req createContentRequest) (*contentResult, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.do("POST", "/rest/api/content", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create content failed: %s: %s", resp.Status, b)
	}
	var result contentResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
