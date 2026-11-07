package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

type dockerHandler func(http.ResponseWriter, *http.Request)

func newTestDockerProvider(t *testing.T, handler dockerHandler) (*DockerProvider, func()) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1.56")
		handler(w, r)
	}))
	cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.56"))
	if err != nil {
		server.Close()
		t.Fatalf("create Docker client: %v", err)
	}

	return &DockerProvider{client: cli, Publish: true}, func() {
		_ = cli.Close()
		server.Close()
	}
}

func writeDockerResponse(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprint(w, body)
}

func TestContainerName(t *testing.T) {
	if got, want := containerName("abc-123"), "workspace-abc-123"; got != want {
		t.Fatalf("containerName() = %q, want %q", got, want)
	}
}

func TestNewDockerProvider(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	t.Setenv("DOCKER_HOST", server.URL)
	t.Setenv("DOCKER_API_VERSION", "1.56")

	provider, err := NewDockerProvider()
	if err != nil {
		t.Fatalf("NewDockerProvider() error = %v", err)
	}
	defer provider.client.Close()

	if provider.client == nil {
		t.Fatal("NewDockerProvider() returned a nil client")
	}
	if !provider.Publish {
		t.Fatal("NewDockerProvider() Publish = false, want true")
	}
}

func TestDockerProviderPull(t *testing.T) {
	tests := []struct {
		name        string
		imageStatus int
		pullStatus  int
		wantErr     bool
		wantPull    bool
	}{
		{name: "image exists"},
		{name: "pull succeeds", imageStatus: http.StatusNotFound, pullStatus: http.StatusOK, wantPull: true},
		{name: "pull fails", imageStatus: http.StatusNotFound, pullStatus: http.StatusInternalServerError, wantErr: true, wantPull: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pullCalled := false
			provider, cleanup := newTestDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/images/test-image/json":
					status := tt.imageStatus
					if status == 0 {
						status = http.StatusOK
					}
					writeDockerResponse(w, status, `{"message":"image lookup"}`)
				case r.Method == http.MethodPost && r.URL.Path == "/images/create":
					pullCalled = true
					if r.URL.Query().Get("fromImage") != "docker.io/library/test-image" {
						t.Errorf("pull fromImage = %q, want docker.io/library/test-image", r.URL.Query().Get("fromImage"))
					}
					status := tt.pullStatus
					if status == 0 {
						status = http.StatusOK
					}
					writeDockerResponse(w, status, `{"status":"downloaded"}`)
				default:
					t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL)
					writeDockerResponse(w, http.StatusNotFound, `{"message":"unexpected request"}`)
				}
			})
			defer cleanup()

			err := provider.pull(context.Background(), "test-image")
			if (err != nil) != tt.wantErr {
				t.Fatalf("pull() error = %v, wantErr %v", err, tt.wantErr)
			}
			if pullCalled != tt.wantPull {
				t.Errorf("ImagePull called = %v, want %v", pullCalled, tt.wantPull)
			}
		})
	}
}

func TestDockerProviderGet(t *testing.T) {
	created := "2025-01-02T03:04:05.123456789Z"
	createdAt, _ := time.Parse(time.RFC3339Nano, created)

	tests := []struct {
		name        string
		status      int
		body        string
		wantErr     error
		wantState   string
		wantIP      string
		wantURL     string
		wantCreated time.Time
	}{
		{
			name:      "inspect populated instance",
			body:      `{"Id":"container-id","Name":"/workspace-uid-1","Created":"` + created + `","State":{"Status":"running"},"NetworkSettings":{"Networks":{"kind":{"IPAddress":"10.10.0.4"}},"Ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"49152"}]}}}`,
			wantState: "running", wantIP: "10.10.0.4", wantURL: "127.0.0.1:49152", wantCreated: createdAt,
		},
		{
			name: "missing optional fields",
			body: `{"Id":"container-id","Name":"workspace-uid-1","Created":"not-a-time"}`,
		},
		{
			name:    "not found",
			status:  http.StatusNotFound,
			body:    `{"message":"no such container"}`,
			wantErr: ErrNotFound,
		},
		{
			name:    "server error",
			status:  http.StatusInternalServerError,
			body:    `{"message":"daemon unavailable"}`,
			wantErr: errors.New("daemon unavailable"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, cleanup := newTestDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/containers/workspace-uid-1/json" {
					t.Errorf("request = %s %s, want GET /containers/workspace-uid-1/json", r.Method, r.URL.Path)
				}
				status := tt.status
				if status == 0 {
					status = http.StatusOK
				}
				writeDockerResponse(w, status, tt.body)
			})
			defer cleanup()

			got, err := provider.Get(context.Background(), "uid-1")
			if tt.wantErr != nil {
				if err == nil {
					t.Fatal("Get() error = nil, want an error")
				}
				if errors.Is(tt.wantErr, ErrNotFound) {
					if !errors.Is(err, ErrNotFound) {
						t.Errorf("Get() error = %v, want ErrNotFound", err)
					}
				} else if !strings.Contains(err.Error(), tt.wantErr.Error()) {
					t.Errorf("Get() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if got.ID != "container-id" || got.Name != "workspace-uid-1" || got.UID != "uid-1" {
				t.Errorf("Get() identity = %#v", got)
			}
			if got.State != tt.wantState || got.IP != tt.wantIP || got.URL != tt.wantURL {
				t.Errorf("Get() state/network = state %q, IP %q, URL %q", got.State, got.IP, got.URL)
			}
			if !got.CreatedAt.Equal(tt.wantCreated) {
				t.Errorf("Get() CreatedAt = %v, want %v", got.CreatedAt, tt.wantCreated)
			}
		})
	}
}

func TestDockerProviderCreate(t *testing.T) {
	tests := []struct {
		name        string
		publish     bool
		imageStatus int
		pullStatus  int
		createCode  int
		startCode   int
		wantErr     string
	}{
		{name: "creates and publishes port", publish: true},
		{name: "creates without publishing port", publish: false},
		{name: "accepts existing container conflict", publish: true, createCode: http.StatusConflict},
		{name: "create failure", publish: true, createCode: http.StatusInternalServerError, wantErr: "create container"},
		{name: "start failure", publish: true, startCode: http.StatusInternalServerError, wantErr: "start container"},
		{name: "image pull failure", publish: true, imageStatus: http.StatusNotFound, pullStatus: http.StatusInternalServerError, wantErr: "pull test-image"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var createBody map[string]any
			var started bool
			inspectCalls := 0
			provider, cleanup := newTestDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/images/test-image/json":
					status := tt.imageStatus
					if status == 0 {
						status = http.StatusOK
					}
					writeDockerResponse(w, status, `{"message":"image lookup"}`)
				case r.Method == http.MethodPost && r.URL.Path == "/images/create":
					code := tt.pullStatus
					if code == 0 {
						code = http.StatusOK
					}
					writeDockerResponse(w, code, `{"status":"downloaded"}`)
				case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
					if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
						t.Errorf("decode create body: %v", err)
					}
					if r.URL.Query().Get("name") != "workspace-uid-1" {
						t.Errorf("create name = %q, want workspace-uid-1", r.URL.Query().Get("name"))
					}
					code := tt.createCode
					if code == 0 {
						code = http.StatusCreated
					}
					writeDockerResponse(w, code, `{"Id":"container-id"}`)
				case r.Method == http.MethodPost && r.URL.Path == "/containers/workspace-uid-1/start":
					started = true
					code := tt.startCode
					if code == 0 {
						code = http.StatusNoContent
					}
					writeDockerResponse(w, code, "")
				case r.Method == http.MethodGet && r.URL.Path == "/containers/workspace-uid-1/json":
					inspectCalls++
					writeDockerResponse(w, http.StatusOK, `{"Id":"container-id","Name":"/workspace-uid-1","State":{"Status":"running"}}`)
				default:
					t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL)
					writeDockerResponse(w, http.StatusNotFound, `{"message":"unexpected request"}`)
				}
			})
			defer cleanup()
			provider.Publish = tt.publish

			got, err := provider.Create(context.Background(), CreateRequest{
				UID: "uid-1", Owner: "owner-1", Image: "test-image", Port: 8080,
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Create() error = %v, want it to contain %q", err, tt.wantErr)
				}
				if inspectCalls != 0 {
					t.Errorf("container inspected %d times after create failure, want 0", inspectCalls)
				}
				if tt.wantErr == "start container" && !started {
					t.Error("container was not started")
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if got.ID != "container-id" || got.UID != "uid-1" || got.State != "running" {
				t.Errorf("Create() instance = %#v", got)
			}
			if !started {
				t.Error("container was not started")
			}
			if inspectCalls != 1 {
				t.Errorf("container inspected %d times, want 1", inspectCalls)
			}

			if createBody["Image"] != "test-image" {
				t.Errorf("create Image = %v, want test-image", createBody["Image"])
			}
			labels, ok := createBody["Labels"].(map[string]any)
			if !ok {
				t.Fatalf("create Labels = %#v, want object", createBody["Labels"])
			}
			for key, want := range map[string]string{
				LabelManagedBy: managedValue, LabeUID: "uid-1", LabeOwner: "owner-1",
			} {
				if labels[key] != want {
					t.Errorf("Config.Labels[%q] = %v, want %q", key, labels[key], want)
				}
			}
			hostConfig, ok := createBody["HostConfig"].(map[string]any)
			if !ok {
				t.Fatalf("create HostConfig = %#v, want object", createBody["HostConfig"])
			}
			if hostConfig["NetworkMode"] != dockerNetwork {
				t.Errorf("HostConfig.NetworkMode = %v, want %q", hostConfig["NetworkMode"], dockerNetwork)
			}
			portBindingsValue, hasPortBindings := hostConfig["PortBindings"]
			if tt.publish && !hasPortBindings {
				t.Fatal("HostConfig.PortBindings missing, want a published port binding")
			}
			if tt.publish {
				bindings, ok := portBindingsValue.(map[string]any)
				if !ok {
					t.Fatalf("HostConfig.PortBindings = %#v, want object", portBindingsValue)
				}
				portBindings, ok := bindings["8080/tcp"].([]any)
				if !ok || len(portBindings) != 1 {
					t.Fatalf("PortBindings[8080/tcp] = %#v, want one binding", bindings["8080/tcp"])
				}
				binding, ok := portBindings[0].(map[string]any)
				if !ok || binding["HostIp"] != "127.0.0.1" {
					t.Errorf("port binding = %#v, want HostIp 127.0.0.1", portBindings[0])
				}
			} else if portBindingsValue != nil {
				bindings, ok := portBindingsValue.(map[string]any)
				if !ok || len(bindings) != 0 {
					t.Errorf("HostConfig.PortBindings = %#v, want no published bindings", portBindingsValue)
				}
			}
		})
	}
}

func TestDockerProviderCreateRejectsInvalidPort(t *testing.T) {
	provider, cleanup := newTestDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/images/test-image/json" {
			writeDockerResponse(w, http.StatusOK, `{}`)
			return
		}
		t.Errorf("unexpected request for invalid port: %s %s", r.Method, r.URL)
		writeDockerResponse(w, http.StatusNotFound, `{"message":"unexpected request"}`)
	})
	defer cleanup()

	_, err := provider.Create(context.Background(), CreateRequest{
		UID: "uid-1", Image: "test-image", Port: 70000,
	})
	if err == nil {
		t.Fatal("Create() error = nil, want invalid-port error")
	}
}

func TestDockerProviderDelete(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{name: "success"},
		{name: "not found", status: http.StatusNotFound, want: ErrNotFound},
		{name: "server error", status: http.StatusInternalServerError, want: errors.New("daemon unavailable")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, cleanup := newTestDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/containers/workspace-uid-1" {
					t.Errorf("request = %s %s, want DELETE /containers/workspace-uid-1", r.Method, r.URL.Path)
				}
				if r.URL.Query().Get("force") != "1" {
					t.Errorf("force query = %q, want 1", r.URL.Query().Get("force"))
				}
				status := tt.status
				if status == 0 {
					status = http.StatusNoContent
				}
				writeDockerResponse(w, status, `{"message":"daemon unavailable"}`)
			})
			defer cleanup()

			err := provider.Delete(context.Background(), "uid-1")
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Delete() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Delete() error = nil, want an error")
			}
			if errors.Is(tt.want, ErrNotFound) {
				if !errors.Is(err, ErrNotFound) {
					t.Errorf("Delete() error = %v, want ErrNotFound", err)
				}
			} else if !strings.Contains(err.Error(), tt.want.Error()) {
				t.Errorf("Delete() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestDockerProviderList(t *testing.T) {
	created := int64(1735787045)
	provider, cleanup := newTestDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/json" {
			t.Errorf("request = %s %s, want GET /containers/json", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("all") != "1" {
			t.Errorf("all query = %q, want 1", r.URL.Query().Get("all"))
		}
		var filters map[string]map[string]bool
		if err := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters); err != nil {
			t.Errorf("decode filters query: %v", err)
		}
		if got := filters["label"]; len(got) != 1 || !got[LabelManagedBy+"="+managedValue] {
			t.Errorf("label filter = %#v, want %q", got, LabelManagedBy+"="+managedValue)
		}
		writeDockerResponse(w, http.StatusOK, `[{"Id":"container-1","State":"running","Created":1735787045,"Labels":{"workspace-uid":"uid-1"}},{"Id":"container-2","State":"exited","Created":0,"Labels":{}}]`)
	})
	defer cleanup()

	got, err := provider.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List() returned %d instances, want 2", len(got))
	}
	if got[0].ID != "container-1" || got[0].UID != "uid-1" || got[0].State != "running" || got[0].CreatedAt.Unix() != created {
		t.Errorf("List()[0] = %#v", got[0])
	}
	if got[1].ID != "container-2" || got[1].UID != "" || got[1].State != "exited" || !got[1].CreatedAt.Equal(time.Unix(0, 0)) {
		t.Errorf("List()[1] = %#v", got[1])
	}
}

func TestDockerProviderListError(t *testing.T) {
	provider, cleanup := newTestDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		writeDockerResponse(w, http.StatusInternalServerError, `{"message":"daemon unavailable"}`)
	})
	defer cleanup()

	_, err := provider.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "daemon unavailable") {
		t.Fatalf("List() error = %v, want daemon error", err)
	}
}
