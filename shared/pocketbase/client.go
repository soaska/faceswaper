package pocketbase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const maxResponseBody = 1000

// Client owns PocketBase admin authentication and serializes token refreshes.
// It is safe to use from multiple bot and worker goroutines.
type Client struct {
	baseURL    string
	identity   string
	password   string
	httpClient *http.Client

	tokenMu   sync.RWMutex
	token     string
	refreshMu sync.Mutex
}

type StatusError struct {
	StatusCode int
	Body       string
}

func (err *StatusError) Error() string {
	return fmt.Sprintf("PocketBase вернул код %d: %s", err.StatusCode, err.Body)
}

func New(baseURL, identity, password string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		identity:   identity,
		password:   password,
		httpClient: httpClient,
	}
}

func (client *Client) BaseURL() string {
	return client.baseURL
}

func (client *Client) Token() string {
	client.tokenMu.RLock()
	defer client.tokenMu.RUnlock()
	return client.token
}

func (client *Client) setToken(token string) {
	client.tokenMu.Lock()
	client.token = token
	client.tokenMu.Unlock()
}

func (client *Client) Authenticate() error {
	payload, err := json.Marshal(map[string]string{
		"identity": client.identity,
		"password": client.password,
	})
	if err != nil {
		return fmt.Errorf("ошибка сериализации данных авторизации: %v", err)
	}

	resp, err := client.httpClient.Post(
		client.baseURL+"/api/admins/auth-with-password",
		"application/json",
		bytes.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("ошибка запроса авторизации PocketBase: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа авторизации: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("авторизация не удалась, код %d: %s", resp.StatusCode, LimitedBody(body))
	}

	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("ошибка разбора ответа авторизации: %v", err)
	}
	if response.Token == "" {
		return fmt.Errorf("PocketBase не вернул токен")
	}
	client.setToken(response.Token)
	return nil
}

// Do sends an authenticated JSON request and retries it once after a serialized
// token refresh. The request payload remains byte-for-byte identical on retry.
func (client *Client) Do(method, requestURL string, payload []byte) ([]byte, error) {
	body, statusCode, err := client.DoAuthenticated(func(token string) ([]byte, int, error) {
		return client.doRequest(method, requestURL, payload, token)
	})
	if err != nil {
		return nil, err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, &StatusError{StatusCode: statusCode, Body: LimitedBody(body)}
	}
	return body, nil
}

// DoAuthenticated applies the same refresh policy to custom requests such as
// streaming multipart uploads. Status validation remains with the caller.
func (client *Client) DoAuthenticated(
	operation func(token string) ([]byte, int, error),
) ([]byte, int, error) {
	token := client.Token()
	body, statusCode, err := operation(token)
	if err != nil {
		return nil, statusCode, err
	}
	if statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden {
		return body, statusCode, nil
	}

	client.refreshMu.Lock()
	defer client.refreshMu.Unlock()
	if client.Token() == token {
		if err := client.Authenticate(); err != nil {
			return nil, statusCode, fmt.Errorf("ошибка обновления авторизации PocketBase: %v", err)
		}
	}
	return operation(client.Token())
}

func (client *Client) doRequest(method, requestURL string, payload []byte, token string) ([]byte, int, error) {
	req, err := http.NewRequest(method, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("ошибка создания запроса PocketBase: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("ошибка запроса PocketBase: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("ошибка чтения ответа PocketBase: %v", err)
	}
	return body, resp.StatusCode, nil
}

func LimitedBody(body []byte) string {
	if len(body) <= maxResponseBody {
		return string(body)
	}
	return string(body[:maxResponseBody]) + "…"
}
