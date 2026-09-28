// Package provider implements the CloudLab Terraform provider.
package provider

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const (
	defaultPortalURL = "https://boss.emulab.net:43794"
	apiBasePath      = ""

	// Poll intervals for experiment readiness.
	pollInterval = 15 * time.Second
	pollTimeout  = 30 * time.Minute
)

// Experiment status strings returned by the Portal API.
// WaitForExperiment polls through any intermediate state until "ready" or "failed".
const (
	StatusReady  = "ready"
	StatusFailed = "failed"
)

// Client is the CloudLab Portal API HTTP client.
type Client struct {
	httpClient *http.Client
	portalURL  string
	token      string
}

// NewClient creates a new CloudLab Portal API client.
func NewClient(portalURL, token string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 16
	transport.MaxConnsPerHost = 16
	transport.IdleConnTimeout = 90 * time.Second
	return &Client{
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		portalURL: portalURL,
		token:     token,
	}
}

// ---------------------------------------------------------------------------
// Experiment types
// ---------------------------------------------------------------------------

// ExperimentCreateRequest represents the body for POST /experiments.
type ExperimentCreateRequest struct {
	Name           string         `json:"name"`
	Project        string         `json:"project"`
	Group          string         `json:"group,omitempty"`
	ProfileName    string         `json:"profile_name"`
	ProfileProject string         `json:"profile_project"`
	Duration       *int64         `json:"duration,omitempty"`
	StartAt        *string        `json:"start_at,omitempty"`
	StopAt         *string        `json:"stop_at,omitempty"`
	ParamsetName   *string        `json:"paramset_name,omitempty"`
	ParamsetOwner  *string        `json:"paramset_owner,omitempty"`
	Bindings       map[string]any `json:"bindings,omitempty"`
	Refspec        *string        `json:"refspec,omitempty"`
	SSHPubKey      *string        `json:"sshpubkey,omitempty"`
}

// ExperimentExtendRequest represents the body for PUT /experiments/{id}.
type ExperimentExtendRequest struct {
	ExpiresAt *string `json:"expires_at,omitempty"`
	ExtendBy  *int64  `json:"extend_by,omitempty"`
	Reason    *string `json:"reason,omitempty"`
}

// ExperimentModifyRequest represents the body for PATCH /experiments/{id}.
type ExperimentModifyRequest struct {
	Bindings map[string]any `json:"bindings"`
}

// AggregateNode represents per-node information at each aggregate.
type AggregateNode struct {
	URN           string `json:"urn"`
	ClientID      string `json:"client_id"`
	Hostname      string `json:"hostname"`
	IPv4          string `json:"ipv4"`
	Status        string `json:"status"`
	State         string `json:"state"`
	RawState      string `json:"rawstate"`
	StartupStatus string `json:"startup_status"`
}

// AggregateStatus represents the status of one aggregate within an experiment.
type AggregateStatus struct {
	URN    string          `json:"urn"`
	Name   string          `json:"name"`
	Status string          `json:"status"`
	Nodes  []AggregateNode `json:"nodes"`
}

// SnapshotStatus represents the status of a snapshot operation.
type SnapshotStatus struct {
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	StatusTimestamp *string `json:"status_timestamp"`
	ImageSize       *int64  `json:"image_size"`
	ImageURN        string  `json:"image_urn"`
	ErrorMessage    *string `json:"error_message"`
}

// ExperimentResponse represents a CloudLab experiment object.
type ExperimentResponse struct {
	ID                 string                     `json:"id"`
	Name               string                     `json:"name"`
	Project            string                     `json:"project"`
	Group              string                     `json:"group"`
	ProfileID          string                     `json:"profile_id"`
	ProfileName        string                     `json:"profile_name"`
	ProfileProject     string                     `json:"profile_project"`
	Creator            string                     `json:"creator"`
	Updater            *string                    `json:"updater"`
	Status             string                     `json:"status"`
	CreatedAt          string                     `json:"created_at"`
	StartAt            *string                    `json:"start_at"`
	StopAt             *string                    `json:"stop_at"`
	StartedAt          *string                    `json:"started_at"`
	ExpiresAt          *string                    `json:"expires_at"`
	URL                string                     `json:"url"`
	WBStoreID          string                     `json:"wbstore_id"`
	RepositoryURL      *string                    `json:"repository_url"`
	RepositoryRefspec  *string                    `json:"repository_refspec"`
	RepositoryHash     *string                    `json:"repository_hash"`
	Bindings           map[string]any             `json:"bindings"`
	Aggregates         map[string]AggregateStatus `json:"aggregates"`
	LastSnapshotStatus *SnapshotStatus            `json:"last_snapshot_status"`
	SSHPubKey          *string                    `json:"sshpubkey"`
}

// ExperimentListResponse represents the GET /experiments response.
type ExperimentListResponse struct {
	Experiments []ExperimentResponse `json:"experiments"`
}

// SnapshotRequest represents the body for POST /experiments/{id}/snapshot/{client_id}.
type SnapshotRequest struct {
	ImageName string `json:"image_name"`
	WholeDisk bool   `json:"whole_disk,omitempty"`
}

// ---------------------------------------------------------------------------
// Profile types
// ---------------------------------------------------------------------------

// ProfileCreateRequest represents the body for POST /profiles.
type ProfileCreateRequest struct {
	Name            string `json:"name"`
	Project         string `json:"project"`
	Script          string `json:"script,omitempty"`
	RepositoryURL   string `json:"repository_url,omitempty"`
	Public          bool   `json:"public"`
	ProjectWritable bool   `json:"project_writable"`
}

// ProfileModifyRequest represents the body for PATCH /profiles/{id}.
type ProfileModifyRequest struct {
	Script          *string `json:"script,omitempty"`
	Public          *bool   `json:"public,omitempty"`
	ProjectWritable *bool   `json:"project_writable,omitempty"`
}

// ProfileVersion represents a single version of a profile.
type ProfileVersion struct {
	ID        string  `json:"id"`
	Version   int64   `json:"version"`
	Updater   string  `json:"updater"`
	CreatedAt string  `json:"created_at"`
	DeletedAt *string `json:"deleted_at"`
	Rspec     string  `json:"rspec"`
	Script    *string `json:"script"`
}

// ProfileResponse represents a CloudLab profile object.
type ProfileResponse struct {
	ID                string                    `json:"id"`
	Name              string                    `json:"name"`
	Version           int64                     `json:"version"`
	Project           string                    `json:"project"`
	Creator           string                    `json:"creator"`
	CreatedAt         string                    `json:"created_at"`
	UpdatedAt         *string                   `json:"updated_at"`
	RepositoryURL     *string                   `json:"repository_url"`
	RepositoryRefspec *string                   `json:"repository_refspec"`
	RepositoryHash    *string                   `json:"repository_hash"`
	RepositoryGithook *string                   `json:"repository_githook"`
	Public            bool                      `json:"public"`
	ProjectWritable   bool                      `json:"project_writable"`
	CurrentVersion    *ProfileVersion           `json:"current_version"`
	ProfileVersions   map[string]ProfileVersion `json:"profile_versions"`
}

// ProfileListResponse represents the GET /profiles response.
type ProfileListResponse struct {
	Profiles []ProfileResponse `json:"profiles"`
}

// ---------------------------------------------------------------------------
// Resgroup types
// ---------------------------------------------------------------------------

// ResgroupNodeType represents a node type reservation in a resgroup.
type ResgroupNodeType struct {
	ResgroupID    string  `json:"resgroup_id,omitempty"`
	URN           string  `json:"urn"`
	ReservationID string  `json:"reservation_id,omitempty"`
	NodeType      string  `json:"nodetype"`
	Count         int64   `json:"count"`
	ApprovedAt    *string `json:"approved_at,omitempty"`
	CanceledAt    *string `json:"canceled_at,omitempty"`
	DeletedAt     *string `json:"deleted_at,omitempty"`
	Error         *string `json:"error,omitempty"`
	ErrorCode     *int64  `json:"errorCode,omitempty"`
}

// ResgroupNodeTypes is a wrapper around a list of node type reservations.
type ResgroupNodeTypes struct {
	NodeTypes []ResgroupNodeType `json:"nodetypes"`
}

// ResgroupRange represents a frequency range reservation in a resgroup.
type ResgroupRange struct {
	ResgroupID    string  `json:"resgroup_id,omitempty"`
	ReservationID string  `json:"reservation_id,omitempty"`
	MinFreq       float64 `json:"min_freq"`
	MaxFreq       float64 `json:"max_freq"`
	ApprovedAt    *string `json:"approved_at,omitempty"`
	CanceledAt    *string `json:"canceled_at,omitempty"`
	Error         *string `json:"error,omitempty"`
	ErrorCode     *int64  `json:"errorCode,omitempty"`
}

// ResgroupRanges is a wrapper around a list of range reservations.
type ResgroupRanges struct {
	Ranges []ResgroupRange `json:"ranges"`
}

// ResgroupRoute represents a named route reservation in a resgroup.
type ResgroupRoute struct {
	ResgroupID    string  `json:"resgroup_id,omitempty"`
	ReservationID string  `json:"reservation_id,omitempty"`
	Name          string  `json:"name"`
	ApprovedAt    *string `json:"approved_at,omitempty"`
	CanceledAt    *string `json:"canceled_at,omitempty"`
}

// ResgroupRoutes is a wrapper around a list of route reservations.
type ResgroupRoutes struct {
	Routes []ResgroupRoute `json:"routes"`
}

// ResgroupReservation is a union type for adding a reservation to a group.
type ResgroupReservation struct {
	NodeType *ResgroupNodeType `json:"nodetype,omitempty"`
	Range    *ResgroupRange    `json:"range,omitempty"`
	Route    *ResgroupRoute    `json:"route,omitempty"`
}

// ResgroupCreateRequest represents the body for POST /resgroups.
type ResgroupCreateRequest struct {
	Project     string             `json:"project"`
	Group       string             `json:"group,omitempty"`
	Reason      string             `json:"reason"`
	StartAt     *string            `json:"start_at,omitempty"`
	ExpiresAt   *string            `json:"expires_at,omitempty"`
	PowderZones *string            `json:"powder_zones,omitempty"`
	NodeTypes   *ResgroupNodeTypes `json:"nodetypes,omitempty"`
	Ranges      *ResgroupRanges    `json:"ranges,omitempty"`
	Routes      *ResgroupRoutes    `json:"routes,omitempty"`
}

// ResgroupResponse represents a CloudLab reservation group object.
type ResgroupResponse struct {
	ID          string             `json:"id"`
	Project     string             `json:"project"`
	Group       string             `json:"group"`
	Reason      string             `json:"reason"`
	Creator     string             `json:"creator"`
	CreatedAt   *string            `json:"created_at"`
	StartAt     *string            `json:"start_at"`
	ExpiresAt   *string            `json:"expires_at"`
	PowderZones *string            `json:"powder_zones"`
	NodeTypes   *ResgroupNodeTypes `json:"nodetypes"`
	Ranges      *ResgroupRanges    `json:"ranges"`
	Routes      *ResgroupRoutes    `json:"routes"`
}

// ResgroupListResponse represents the GET /resgroups response.
type ResgroupListResponse struct {
	Resgroups []ResgroupResponse `json:"resgroups"`
}

// ResgroupSearchRequest represents the body for POST /resgroups/search.
type ResgroupSearchRequest struct {
	Project   string             `json:"project"`
	Group     string             `json:"group,omitempty"`
	NodeTypes *ResgroupNodeTypes `json:"nodetypes,omitempty"`
	Ranges    *ResgroupRanges    `json:"ranges,omitempty"`
	Routes    *ResgroupRoutes    `json:"routes,omitempty"`
}

// ResgroupSearchResult represents the response from POST /resgroups/search.
type ResgroupSearchResult struct {
	StartAt   string `json:"start_at"`
	ExpiresAt string `json:"expires_at"`
}

// ---------------------------------------------------------------------------
// Manifest types (legacy, kept for backward compat)
// ---------------------------------------------------------------------------

// ManifestListResponse represents the GET /experiments/{id}/manifests response.
type ManifestListResponse struct {
	Manifests []ManifestEntry `json:"manifests"`
}

// ManifestEntry represents a single manifest for an aggregate.
type ManifestEntry struct {
	Aggregate string      `json:"aggregate"`
	Nodes     []NodeEntry `json:"nodes"`
}

// NodeEntry represents a node in a manifest.
type NodeEntry struct {
	ClientID   string          `json:"client_id"`
	Hostname   string          `json:"hostname"`
	Interfaces []NodeInterface `json:"interfaces"`
}

// NodeInterface represents a network interface on a node.
type NodeInterface struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

// APIError represents an error response from the Portal API.
type APIError struct {
	StatusCode int
	Message    string   `json:"error"`
	Errors     []string `json:"errors"`
}

func (e *APIError) Error() string {
	if len(e.Errors) > 0 {
		return fmt.Sprintf("cloudlab api error (HTTP %d): %s: %v", e.StatusCode, e.Message, e.Errors)
	}
	return fmt.Sprintf("cloudlab api error (HTTP %d): %s", e.StatusCode, e.Message)
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

// retryDelays defines the base wait between successive retry attempts. Random
// jitter is added so requests that hit the Portal's transient token race do not
// wake up together and collide again.
var retryDelays = []time.Duration{750 * time.Millisecond, 1500 * time.Millisecond}

const retryJitterMax = 500 * time.Millisecond

func jitteredRetryDelay(base time.Duration) time.Duration {
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(retryJitterMax)+1))
	if err != nil {
		return base
	}
	return base + time.Duration(value.Int64())
}

// isRetryable reports whether err warrants a retry.
// Three categories are retried:
//   - HTTP 429 Too Many Requests — the server explicitly asked us to back off.
//   - HTTP 401 "No such user" — a transient race in the Portal's session
//     layer when two requests with the same token arrive too close together;
//     a jittered retry succeeds. Other 401s (bad token) are not retried.
//   - net.Error (connection refused, DNS failure, transport-level timeout) — the
//     request never reached the server, so retrying is safe regardless of method.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == http.StatusUnauthorized {
			return strings.Contains(apiErr.Message, "No such user")
		}
		return apiErr.StatusCode == http.StatusTooManyRequests
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// isSearchRetryable extends the generic policy for the read-only reservation
// predictor. The Portal occasionally returns a transient 5xx while many
// predictions overlap; retrying this endpoint is safe because it does not
// create or modify a reservation.
func isSearchRetryable(err error) bool {
	if isRetryable(err) {
		return true
	}
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= http.StatusInternalServerError
}

func (c *Client) doRequest(ctx context.Context, method, path string, body any) ([]byte, error) {
	return c.doRequestWithRetryPolicy(ctx, method, path, body, isRetryable)
}

func (c *Client) doRequestWithRetryPolicy(
	ctx context.Context,
	method string,
	path string,
	body any,
	shouldRetry func(error) bool,
) ([]byte, error) {
	var bodyBytes []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyBytes = data
	}

	var lastErr error
	for attempt := 0; attempt <= len(retryDelays); attempt++ {
		if attempt > 0 {
			delay := jitteredRetryDelay(retryDelays[attempt-1])
			tflog.Debug(ctx, "Waiting to retry CloudLab API request", map[string]any{
				"method":   method,
				"path":     path,
				"attempt":  attempt + 1,
				"delay_ms": delay.Milliseconds(),
			})
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		tflog.Debug(ctx, "Sending CloudLab API request", map[string]any{
			"method":  method,
			"path":    path,
			"attempt": attempt + 1,
		})
		started := time.Now()
		respBody, err := c.doOnce(ctx, method, path, bodyBytes)
		if err == nil {
			tflog.Debug(ctx, "CloudLab API request completed", map[string]any{
				"method":      method,
				"path":        path,
				"attempt":     attempt + 1,
				"duration_ms": time.Since(started).Milliseconds(),
			})
			return respBody, nil
		}
		lastErr = err
		retryable := shouldRetry(err)
		tflog.Debug(ctx, "CloudLab API request failed", map[string]any{
			"method":      method,
			"path":        path,
			"attempt":     attempt + 1,
			"duration_ms": time.Since(started).Milliseconds(),
			"retryable":   retryable,
			"error":       err.Error(),
		})
		if !retryable {
			return nil, err
		}
	}
	return nil, lastErr
}

// doOnce performs a single HTTP request without retry logic.
func (c *Client) doOnce(ctx context.Context, method, path string, bodyBytes []byte) ([]byte, error) {
	var reqBody io.Reader
	if bodyBytes != nil {
		reqBody = bytes.NewReader(bodyBytes)
	}

	url := c.portalURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	// Set GetBody so Go's http.Client can re-send the body on 307/308 redirects.
	// The CloudLab API server (uvicorn) redirects /foo to /foo/ and 307 redirects
	// require the original method and body to be preserved.
	if bodyBytes != nil {
		snapshot := bodyBytes
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(snapshot)), nil
		}
	}

	req.Header.Set("X-Api-Token", c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		// Some Portal errors use {"error": ...}, others (e.g. the FastAPI
		// layer) use {"detail": ...}; keep the raw body when neither parses.
		if jsonErr := json.Unmarshal(respBody, apiErr); jsonErr != nil || apiErr.Message == "" {
			apiErr.Message = string(respBody)
		}
		return nil, apiErr
	}

	return respBody, nil
}

// ---------------------------------------------------------------------------
// Experiment API methods
// ---------------------------------------------------------------------------

// CreateExperiment creates a new experiment on CloudLab.
func (c *Client) CreateExperiment(ctx context.Context, req *ExperimentCreateRequest) (*ExperimentResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments", req)
	if err != nil {
		return nil, err
	}
	var exp ExperimentResponse
	if err := json.Unmarshal(body, &exp); err != nil {
		return nil, fmt.Errorf("failed to parse experiment response: %w", err)
	}
	return &exp, nil
}

// GetExperiment retrieves an experiment by its Portal ID (name:project or UUID).
func (c *Client) GetExperiment(ctx context.Context, experimentID string) (*ExperimentResponse, error) {
	body, err := c.doRequest(ctx, http.MethodGet, "/experiments/"+experimentID, nil)
	if err != nil {
		return nil, err
	}
	var exp ExperimentResponse
	if err := json.Unmarshal(body, &exp); err != nil {
		return nil, fmt.Errorf("failed to parse experiment response: %w", err)
	}
	return &exp, nil
}

// ListExperiments lists experiments, optionally filtered by project.
func (c *Client) ListExperiments(ctx context.Context, project string) ([]ExperimentResponse, error) {
	path := "/experiments"
	if project != "" {
		path += "?project=" + project
	}
	body, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var list ExperimentListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("failed to parse experiment list response: %w", err)
	}
	return list.Experiments, nil
}

// ExtendExperiment extends a running experiment's lifetime (PUT /experiments/{id}).
func (c *Client) ExtendExperiment(ctx context.Context, experimentID string, req *ExperimentExtendRequest) (*ExperimentResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPut, "/experiments/"+experimentID, req)
	if err != nil {
		return nil, err
	}
	var exp ExperimentResponse
	if err := json.Unmarshal(body, &exp); err != nil {
		return nil, fmt.Errorf("failed to parse experiment response: %w", err)
	}
	return &exp, nil
}

// ModifyExperiment modifies bindings on a running experiment (PATCH /experiments/{id}).
func (c *Client) ModifyExperiment(ctx context.Context, experimentID string, req *ExperimentModifyRequest) (*ExperimentResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPatch, "/experiments/"+experimentID, req)
	if err != nil {
		return nil, err
	}
	var exp ExperimentResponse
	if err := json.Unmarshal(body, &exp); err != nil {
		return nil, fmt.Errorf("failed to parse experiment response: %w", err)
	}
	return &exp, nil
}

// DeleteExperiment deletes (terminates) an experiment.
func (c *Client) DeleteExperiment(ctx context.Context, experimentID string) error {
	_, err := c.doRequest(ctx, http.MethodDelete, "/experiments/"+experimentID, nil)
	return err
}

// WaitForExperiment polls until the experiment reaches "ready" or "failed" status.
func (c *Client) WaitForExperiment(ctx context.Context, experimentID string) (*ExperimentResponse, error) {
	deadline := time.Now().Add(pollTimeout)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for experiment %s to become ready", experimentID)
		}

		exp, err := c.GetExperiment(ctx, experimentID)
		if err != nil {
			return nil, fmt.Errorf("error polling experiment status: %w", err)
		}

		switch exp.Status {
		case StatusReady:
			return exp, nil
		case StatusFailed:
			return nil, fmt.Errorf("experiment %s failed to start", experimentID)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// GetManifests retrieves the manifests for a running experiment.
//
// Deprecated: Use GetRawManifests for the correct XML-based response format.
func (c *Client) GetManifests(ctx context.Context, experimentID string) ([]ManifestEntry, error) {
	body, err := c.doRequest(ctx, http.MethodGet, "/experiments/"+experimentID+"/manifests", nil)
	if err != nil {
		return nil, err
	}
	var list ManifestListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("failed to parse manifest response: %w", err)
	}
	return list.Manifests, nil
}

// GetRawManifests retrieves the manifests for a running experiment as a map of
// aggregate URN -> RSpec XML string. The CloudLab API returns RSpec XML documents,
// not the structured JSON format.
func (c *Client) GetRawManifests(ctx context.Context, experimentID string) (map[string]string, error) {
	body, err := c.doRequest(ctx, http.MethodGet, "/experiments/"+experimentID+"/manifests", nil)
	if err != nil {
		return nil, err
	}
	var result map[string]string
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse manifest response: %w", err)
	}
	return result, nil
}

// GetExperimentNode retrieves info about a specific node in an experiment.
func (c *Client) GetExperimentNode(ctx context.Context, experimentID, clientID string) (*AggregateNode, error) {
	body, err := c.doRequest(ctx, http.MethodGet, "/experiments/"+experimentID+"/node/"+clientID, nil)
	if err != nil {
		return nil, err
	}
	var node AggregateNode
	if err := json.Unmarshal(body, &node); err != nil {
		return nil, fmt.Errorf("failed to parse node response: %w", err)
	}
	return &node, nil
}

// RebootExperimentNodes reboots all nodes in an experiment.
func (c *Client) RebootExperimentNodes(ctx context.Context, experimentID string) (*ExperimentResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/nodes/reboot", nil)
	if err != nil {
		return nil, err
	}
	var exp ExperimentResponse
	if err := json.Unmarshal(body, &exp); err != nil {
		return nil, fmt.Errorf("failed to parse experiment response: %w", err)
	}
	return &exp, nil
}

// ReloadExperimentNodes reloads all nodes in an experiment.
func (c *Client) ReloadExperimentNodes(ctx context.Context, experimentID string) (*ExperimentResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/nodes/reload", nil)
	if err != nil {
		return nil, err
	}
	var exp ExperimentResponse
	if err := json.Unmarshal(body, &exp); err != nil {
		return nil, fmt.Errorf("failed to parse experiment response: %w", err)
	}
	return &exp, nil
}

// StartExperimentNodes starts all nodes in an experiment.
func (c *Client) StartExperimentNodes(ctx context.Context, experimentID string) (*ExperimentResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/nodes/start", nil)
	if err != nil {
		return nil, err
	}
	var exp ExperimentResponse
	if err := json.Unmarshal(body, &exp); err != nil {
		return nil, fmt.Errorf("failed to parse experiment response: %w", err)
	}
	return &exp, nil
}

// StopExperimentNodes stops all nodes in an experiment.
func (c *Client) StopExperimentNodes(ctx context.Context, experimentID string) (*ExperimentResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/nodes/stop", nil)
	if err != nil {
		return nil, err
	}
	var exp ExperimentResponse
	if err := json.Unmarshal(body, &exp); err != nil {
		return nil, fmt.Errorf("failed to parse experiment response: %w", err)
	}
	return &exp, nil
}

// PowercycleExperimentNodes power-cycles all nodes in an experiment.
func (c *Client) PowercycleExperimentNodes(ctx context.Context, experimentID string) (*ExperimentResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/nodes/powercycle", nil)
	if err != nil {
		return nil, err
	}
	var exp ExperimentResponse
	if err := json.Unmarshal(body, &exp); err != nil {
		return nil, fmt.Errorf("failed to parse experiment response: %w", err)
	}
	return &exp, nil
}

// RebootExperimentNode reboots a single node in an experiment.
func (c *Client) RebootExperimentNode(ctx context.Context, experimentID, clientID string) (*AggregateNode, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/node/"+clientID+"/reboot", nil)
	if err != nil {
		return nil, err
	}
	var node AggregateNode
	if err := json.Unmarshal(body, &node); err != nil {
		return nil, fmt.Errorf("failed to parse node response: %w", err)
	}
	return &node, nil
}

// ReloadExperimentNode reloads a single node in an experiment.
func (c *Client) ReloadExperimentNode(ctx context.Context, experimentID, clientID string) (*AggregateNode, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/node/"+clientID+"/reload", nil)
	if err != nil {
		return nil, err
	}
	var node AggregateNode
	if err := json.Unmarshal(body, &node); err != nil {
		return nil, fmt.Errorf("failed to parse node response: %w", err)
	}
	return &node, nil
}

// StartExperimentNode starts a stopped node in an experiment.
func (c *Client) StartExperimentNode(ctx context.Context, experimentID, clientID string) (*AggregateNode, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/node/"+clientID+"/start", nil)
	if err != nil {
		return nil, err
	}
	var node AggregateNode
	if err := json.Unmarshal(body, &node); err != nil {
		return nil, fmt.Errorf("failed to parse node response: %w", err)
	}
	return &node, nil
}

// StopExperimentNode stops a single node in an experiment.
func (c *Client) StopExperimentNode(ctx context.Context, experimentID, clientID string) (*AggregateNode, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/node/"+clientID+"/stop", nil)
	if err != nil {
		return nil, err
	}
	var node AggregateNode
	if err := json.Unmarshal(body, &node); err != nil {
		return nil, fmt.Errorf("failed to parse node response: %w", err)
	}
	return &node, nil
}

// PowercycleExperimentNode power-cycles a single node in an experiment.
func (c *Client) PowercycleExperimentNode(ctx context.Context, experimentID, clientID string) (*AggregateNode, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/node/"+clientID+"/powercycle", nil)
	if err != nil {
		return nil, err
	}
	var node AggregateNode
	if err := json.Unmarshal(body, &node); err != nil {
		return nil, fmt.Errorf("failed to parse node response: %w", err)
	}
	return &node, nil
}

// ConnectExperimentVlan connects a shared VLAN in one experiment to another.
func (c *Client) ConnectExperimentVlan(ctx context.Context, experimentID, sourceLan, targetID, targetLan string) error {
	path := fmt.Sprintf("/experiments/%s/vlan/%s/connect?target_id=%s&target_lan=%s",
		experimentID, sourceLan, targetID, targetLan)
	_, err := c.doRequest(ctx, http.MethodPost, path, nil)
	return err
}

// DisconnectExperimentVlan disconnects a shared VLAN in an experiment.
func (c *Client) DisconnectExperimentVlan(ctx context.Context, experimentID, sourceLan string) error {
	_, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/vlan/"+sourceLan+"/disconnect", nil)
	return err
}

// StartSnapshot initiates a snapshot of a node in an experiment.
func (c *Client) StartSnapshot(ctx context.Context, experimentID, clientID string, req *SnapshotRequest) (*SnapshotStatus, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/experiments/"+experimentID+"/snapshot/"+clientID, req)
	if err != nil {
		return nil, err
	}
	var status SnapshotStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("failed to parse snapshot status response: %w", err)
	}
	return &status, nil
}

// GetSnapshotStatus retrieves the status of a snapshot operation.
func (c *Client) GetSnapshotStatus(ctx context.Context, experimentID, snapshotID string) (*SnapshotStatus, error) {
	body, err := c.doRequest(ctx, http.MethodGet, "/experiments/"+experimentID+"/snapshot/"+snapshotID, nil)
	if err != nil {
		return nil, err
	}
	var status SnapshotStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("failed to parse snapshot status response: %w", err)
	}
	return &status, nil
}

// ---------------------------------------------------------------------------
// Profile API methods
// ---------------------------------------------------------------------------

// CreateProfile creates a new experiment profile.
func (c *Client) CreateProfile(ctx context.Context, req *ProfileCreateRequest) (*ProfileResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/profiles", req)
	if err != nil {
		return nil, err
	}
	var profile ProfileResponse
	if err := json.Unmarshal(body, &profile); err != nil {
		return nil, fmt.Errorf("failed to parse profile response: %w", err)
	}
	return &profile, nil
}

// GetProfile retrieves a profile by its Portal ID.
func (c *Client) GetProfile(ctx context.Context, profileID string) (*ProfileResponse, error) {
	body, err := c.doRequest(ctx, http.MethodGet, "/profiles/"+profileID, nil)
	if err != nil {
		return nil, err
	}
	var profile ProfileResponse
	if err := json.Unmarshal(body, &profile); err != nil {
		return nil, fmt.Errorf("failed to parse profile response: %w", err)
	}
	return &profile, nil
}

// ListProfiles lists all profiles, optionally filtered by project.
func (c *Client) ListProfiles(ctx context.Context, project string) ([]ProfileResponse, error) {
	path := "/profiles"
	if project != "" {
		path += "?project=" + project
	}
	body, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var list ProfileListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("failed to parse profile list response: %w", err)
	}
	return list.Profiles, nil
}

// ModifyProfile modifies mutable profile attributes (PATCH /profiles/{id}).
func (c *Client) ModifyProfile(ctx context.Context, profileID string, req *ProfileModifyRequest) (*ProfileResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPatch, "/profiles/"+profileID, req)
	if err != nil {
		return nil, err
	}
	var profile ProfileResponse
	if err := json.Unmarshal(body, &profile); err != nil {
		return nil, fmt.Errorf("failed to parse profile response: %w", err)
	}
	return &profile, nil
}

// UpdateProfile triggers an update on a repository-backed profile (PUT /profiles/{id}).
func (c *Client) UpdateProfile(ctx context.Context, profileID string) (*ProfileResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPut, "/profiles/"+profileID, nil)
	if err != nil {
		return nil, err
	}
	var profile ProfileResponse
	if err := json.Unmarshal(body, &profile); err != nil {
		return nil, fmt.Errorf("failed to parse profile response: %w", err)
	}
	return &profile, nil
}

// DeleteProfile deletes a profile.
func (c *Client) DeleteProfile(ctx context.Context, profileID string) error {
	_, err := c.doRequest(ctx, http.MethodDelete, "/profiles/"+profileID, nil)
	return err
}

// GetProfileVersion retrieves a specific version of a profile.
func (c *Client) GetProfileVersion(ctx context.Context, profileID, versionID string) (*ProfileResponse, error) {
	body, err := c.doRequest(ctx, http.MethodGet, "/profiles/"+profileID+"/versions/"+versionID, nil)
	if err != nil {
		return nil, err
	}
	var profile ProfileResponse
	if err := json.Unmarshal(body, &profile); err != nil {
		return nil, fmt.Errorf("failed to parse profile version response: %w", err)
	}
	return &profile, nil
}

// DeleteProfileVersion deletes a specific version of a profile.
func (c *Client) DeleteProfileVersion(ctx context.Context, profileID, versionID string) error {
	_, err := c.doRequest(ctx, http.MethodDelete, "/profiles/"+profileID+"/versions/"+versionID, nil)
	return err
}

// ---------------------------------------------------------------------------
// Resgroup API methods
// ---------------------------------------------------------------------------

// CreateResgroup creates a new reservation group.
func (c *Client) CreateResgroup(ctx context.Context, req *ResgroupCreateRequest, durationHours *int64) (*ResgroupResponse, error) {
	path := "/resgroups"
	if durationHours != nil {
		path += fmt.Sprintf("?duration=%d", *durationHours)
	}
	body, err := c.doRequest(ctx, http.MethodPost, path, req)
	if err != nil {
		return nil, err
	}
	var rg ResgroupResponse
	if err := json.Unmarshal(body, &rg); err != nil {
		return nil, fmt.Errorf("failed to parse resgroup response: %w", err)
	}
	return &rg, nil
}

// GetResgroup retrieves a reservation group by ID.
func (c *Client) GetResgroup(ctx context.Context, resgroupID string) (*ResgroupResponse, error) {
	body, err := c.doRequest(ctx, http.MethodGet, "/resgroups/"+resgroupID, nil)
	if err != nil {
		return nil, err
	}
	var rg ResgroupResponse
	if err := json.Unmarshal(body, &rg); err != nil {
		return nil, fmt.Errorf("failed to parse resgroup response: %w", err)
	}
	return &rg, nil
}

// ModifyResgroup modifies an existing reservation group (PUT /resgroups/{id}).
func (c *Client) ModifyResgroup(ctx context.Context, resgroupID string, req *ResgroupCreateRequest, durationHours *int64) (*ResgroupResponse, error) {
	path := "/resgroups/" + resgroupID
	if durationHours != nil {
		path += fmt.Sprintf("?duration=%d", *durationHours)
	}
	body, err := c.doRequest(ctx, http.MethodPut, path, req)
	if err != nil {
		return nil, err
	}
	var rg ResgroupResponse
	if err := json.Unmarshal(body, &rg); err != nil {
		return nil, fmt.Errorf("failed to parse resgroup response: %w", err)
	}
	return &rg, nil
}

// DeleteResgroup deletes a reservation group.
func (c *Client) DeleteResgroup(ctx context.Context, resgroupID string) error {
	_, err := c.doRequest(ctx, http.MethodDelete, "/resgroups/"+resgroupID, nil)
	return err
}

// SearchResgroup searches for a free time slot for a resgroup.
func (c *Client) SearchResgroup(ctx context.Context, req *ResgroupSearchRequest, durationHours int64) (*ResgroupSearchResult, error) {
	path := fmt.Sprintf("/resgroups/search?duration=%d", durationHours)
	body, err := c.doRequestWithRetryPolicy(ctx, http.MethodPost, path, req, isSearchRetryable)
	if err != nil {
		return nil, err
	}
	var result ResgroupSearchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse resgroup search result: %w", err)
	}
	return &result, nil
}

// AddResgroupReservation adds a reservation to an existing resgroup.
func (c *Client) AddResgroupReservation(ctx context.Context, resgroupID string, req *ResgroupReservation) (*ResgroupResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/resgroups/"+resgroupID+"/reservations", req)
	if err != nil {
		return nil, err
	}
	var rg ResgroupResponse
	if err := json.Unmarshal(body, &rg); err != nil {
		return nil, fmt.Errorf("failed to parse resgroup response: %w", err)
	}
	return &rg, nil
}

// DeleteResgroupReservation removes a reservation from a resgroup.
func (c *Client) DeleteResgroupReservation(ctx context.Context, resgroupID, reservationID string) error {
	_, err := c.doRequest(ctx, http.MethodDelete, "/resgroups/"+resgroupID+"/reservations/"+reservationID, nil)
	return err
}
