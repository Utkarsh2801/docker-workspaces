package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	workspacesv1alpha1 "github.com/utkarsh2801/docker-workspaces/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	dashboardOrigin    = "https://workspaces.localhost.pomerium.io"
	testAliceEmail     = "alice@example.com"
	testAliceWorkspace = "alice-workspace"
	jsonContentType    = "application/json"
)

func newTestServer(t *testing.T, objects ...client.Object) (*Server, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register core API types: %v", err)
	}
	if err := workspacesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("register Workspace API types: %v", err)
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return New(k8sClient, ":0", dashboardOrigin), k8sClient
}

func TestDashboardPageIncludesWorkspaceControlsAndLogout(t *testing.T) {
	server, _ := newTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	for _, want := range []string{
		"Create workspace",
		"Your environments",
		"refresh-button",
		"deleteWorkspace",
		"codercom/code-server",
		"https://authenticate.localhost.pomerium.io/.pomerium/sign_out?pomerium_redirect_uri=https%3A%2F%2Fauthenticate.localhost.pomerium.io",
	} {
		if !bytes.Contains(response.Body.Bytes(), []byte(want)) {
			t.Errorf("dashboard page does not contain %q", want)
		}
	}
	if bytes.Contains(response.Body.Bytes(), []byte("traefik/whoami")) {
		t.Error("dashboard page offers Traefik")
	}
}

func TestWorkspaceAPIRequiresPomeriumIdentity(t *testing.T) {
	server, _ := newTestServer(t)

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			path := "/api/workspaces"
			if method == http.MethodDelete {
				path += "/mine"
			}
			request := httptest.NewRequest(method, path, bytes.NewBufferString(`{"name":"mine"}`))
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Errorf("API status = %d, want %d", response.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestListWorkspacesOnlyReturnsAuthenticatedOwner(t *testing.T) {
	server, _ := newTestServer(t,
		&workspacesv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: testAliceWorkspace, Namespace: Namespace},
			Spec:       workspacesv1alpha1.WorkspaceSpec{Owner: testAliceEmail},
			Status:     workspacesv1alpha1.WorkspaceStatus{Phase: workspacesv1alpha1.PhaseRunning, URL: "https://alice-workspace.localhost.pomerium.io"},
		},
		&workspacesv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "bob-workspace", Namespace: Namespace},
			Spec:       workspacesv1alpha1.WorkspaceSpec{Owner: "bob@example.com"},
		},
		&workspacesv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "other-namespace", Namespace: "other"},
			Spec:       workspacesv1alpha1.WorkspaceSpec{Owner: testAliceEmail},
		},
	)
	request := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
	request.Header.Set(IdentityEmailHeader, "ALICE@example.com")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET workspaces status = %d, want %d: %s", response.Code, http.StatusOK, response.Body)
	}
	var got []workspaceSummary
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode workspace list: %v", err)
	}
	if len(got) != 1 || got[0].Name != testAliceWorkspace || got[0].URL != "https://alice-workspace.localhost.pomerium.io" {
		t.Errorf("workspace list = %+v, want only Alice's default-namespace Workspace", got)
	}
}

func TestCreateWorkspaceUsesAuthenticatedOwner(t *testing.T) {
	server, k8sClient := newTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/workspaces", bytes.NewBufferString(`{"name":"my-workspace","image":"codercom/code-server"}`))
	request.Header.Set(IdentityEmailHeader, "Alice@Example.com")
	request.Header.Set("Origin", dashboardOrigin)
	request.Header.Set("Content-Type", jsonContentType)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("POST workspace status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body)
	}

	var got workspacesv1alpha1.Workspace
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "my-workspace", Namespace: Namespace}, &got); err != nil {
		t.Fatalf("get created Workspace: %v", err)
	}
	if got.Spec.Owner != testAliceEmail {
		t.Errorf("Workspace owner = %q, want authenticated identity", got.Spec.Owner)
	}
	if got.Spec.Image != "codercom/code-server" || got.Spec.Port != 8080 {
		t.Errorf("Workspace spec = %+v, want code-server image and port 8080", got.Spec)
	}
	if want := []string{"--bind-addr", "0.0.0.0:8080", "--auth", "none"}; !reflect.DeepEqual(got.Spec.Args, want) {
		t.Errorf("Workspace args = %v, want %v", got.Spec.Args, want)
	}
}

func TestCreateWorkspaceDefaultsToCodeServer(t *testing.T) {
	server, k8sClient := newTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/workspaces", bytes.NewBufferString(`{"name":"default-image"}`))
	request.Header.Set(IdentityEmailHeader, testAliceEmail)
	request.Header.Set("Origin", dashboardOrigin)
	request.Header.Set("Content-Type", jsonContentType)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("POST workspace status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body)
	}
	var got workspacesv1alpha1.Workspace
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "default-image", Namespace: Namespace}, &got); err != nil {
		t.Fatalf("get created Workspace: %v", err)
	}
	if got.Spec.Image != DefaultImage || got.Spec.Port != 8080 {
		t.Errorf("Workspace spec = %+v, want default image %q and port 8080", got.Spec, DefaultImage)
	}
	if want := []string{"--bind-addr", "0.0.0.0:8080", "--auth", "none"}; !reflect.DeepEqual(got.Spec.Args, want) {
		t.Errorf("Workspace args = %v, want %v", got.Spec.Args, want)
	}
}

func TestDeleteWorkspaceRequiresOwnerAndSameOrigin(t *testing.T) {
	tests := []struct {
		name       string
		email      string
		origin     string
		workspace  *workspacesv1alpha1.Workspace
		wantStatus int
	}{
		{
			name:   "owner can delete",
			email:  testAliceEmail,
			origin: dashboardOrigin,
			workspace: &workspacesv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: testAliceWorkspace, Namespace: Namespace},
				Spec:       workspacesv1alpha1.WorkspaceSpec{Owner: testAliceEmail},
			},
			wantStatus: http.StatusAccepted,
		},
		{
			name:   "another owner is hidden",
			email:  testAliceEmail,
			origin: dashboardOrigin,
			workspace: &workspacesv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "bob-workspace", Namespace: Namespace},
				Spec:       workspacesv1alpha1.WorkspaceSpec{Owner: "bob@example.com"},
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing workspace",
			email:      testAliceEmail,
			origin:     dashboardOrigin,
			wantStatus: http.StatusNotFound,
		},
		{
			name:   "foreign origin",
			email:  testAliceEmail,
			origin: "https://attacker.example",
			workspace: &workspacesv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: testAliceWorkspace, Namespace: Namespace},
				Spec:       workspacesv1alpha1.WorkspaceSpec{Owner: testAliceEmail},
			},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := []client.Object{}
			name := "missing"
			if tt.workspace != nil {
				objects = append(objects, tt.workspace)
				name = tt.workspace.Name
			}
			server, k8sClient := newTestServer(t, objects...)
			request := httptest.NewRequest(http.MethodDelete, "/api/workspaces/"+name, nil)
			request.Header.Set(IdentityEmailHeader, tt.email)
			request.Header.Set("Origin", tt.origin)
			response := httptest.NewRecorder()

			server.Handler().ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("DELETE status = %d, want %d: %s", response.Code, tt.wantStatus, response.Body)
			}
			if tt.workspace != nil {
				var got workspacesv1alpha1.Workspace
				err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(tt.workspace), &got)
				if tt.wantStatus == http.StatusAccepted && err == nil {
					t.Error("Workspace still exists after successful deletion")
				}
				if tt.wantStatus != http.StatusAccepted && err != nil {
					t.Errorf("Workspace was unexpectedly removed: %v", err)
				}
			}
		})
	}
}

func TestCreateWorkspaceRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		origin      string
		contentType string
		wantStatus  int
	}{
		{name: "invalid workspace name", body: `{"name":"Not Valid"}`, origin: dashboardOrigin, contentType: jsonContentType, wantStatus: http.StatusBadRequest},
		{name: "unapproved image", body: `{"name":"mine","image":"nginx"}`, origin: dashboardOrigin, contentType: jsonContentType, wantStatus: http.StatusBadRequest},
		{name: "foreign origin", body: `{"name":"mine"}`, origin: "https://attacker.example", contentType: jsonContentType, wantStatus: http.StatusForbidden},
		{name: "wrong content type", body: `{"name":"mine"}`, origin: dashboardOrigin, contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
		{name: "unknown request field", body: `{"name":"mine","owner":"bob@example.com"}`, origin: dashboardOrigin, contentType: jsonContentType, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, k8sClient := newTestServer(t)
			request := httptest.NewRequest(http.MethodPost, "/api/workspaces", bytes.NewBufferString(tt.body))
			request.Header.Set(IdentityEmailHeader, testAliceEmail)
			request.Header.Set("Origin", tt.origin)
			request.Header.Set("Content-Type", tt.contentType)
			response := httptest.NewRecorder()

			server.Handler().ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("POST status = %d, want %d: %s", response.Code, tt.wantStatus, response.Body)
			}

			var workspaces workspacesv1alpha1.WorkspaceList
			if err := k8sClient.List(context.Background(), &workspaces); err != nil {
				t.Fatalf("list workspaces after rejected request: %v", err)
			}
			if len(workspaces.Items) != 0 {
				t.Errorf("rejected request created Workspace(s): %+v", workspaces.Items)
			}
		})
	}
}

func TestCreateWorkspaceReportsNameConflict(t *testing.T) {
	existing := &workspacesv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: Namespace},
		Spec:       workspacesv1alpha1.WorkspaceSpec{Owner: testAliceEmail},
	}
	server, _ := newTestServer(t, existing)
	request := httptest.NewRequest(http.MethodPost, "/api/workspaces", bytes.NewBufferString(`{"name":"mine"}`))
	request.Header.Set(IdentityEmailHeader, testAliceEmail)
	request.Header.Set("Origin", dashboardOrigin)
	request.Header.Set("Content-Type", jsonContentType)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Errorf("POST existing Workspace status = %d, want %d", response.Code, http.StatusConflict)
	}
}
