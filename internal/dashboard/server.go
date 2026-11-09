package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	workspacesv1alpha1 "github.com/utkarsh2801/docker-workspaces/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	IdentityEmailHeader = "X-Pomerium-Claim-Email"
	Namespace           = "default"
	DefaultImage        = "codercom/code-server"
	Port                = ":8082"
)

var (
	workspaceNamePattern = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)
	allowedImages        = map[string]struct{}{
		"codercom/code-server": {},
	}
)

type Server struct {
	client  client.Client
	address string
	origin  string
}

func New(k8sClient client.Client, address, origin string) *Server {
	return &Server{client: k8sClient, address: address, origin: origin}
}

func (s *Server) NeedLeaderElection() bool {
	return false
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/workspaces", s.handleWorkspaces)
	mux.HandleFunc("/api/workspaces/{name}", s.handleWorkspace)
	return mux
}

func (s *Server) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return fmt.Errorf("listen for workspace dashboard on %s: %w", s.address, err)
	}
	server := &http.Server{
		Addr:              s.address,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	shutdownErr := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr <- server.Shutdown(shutdownCtx)
	}()

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve workspace dashboard: %w", err)
	}
	if err := <-shutdownErr; err != nil {
		return fmt.Errorf("shut down workspace dashboard: %w", err)
	}
	return nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(indexHTML))
}

func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	email, ok := authenticatedEmail(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.listWorkspaces(w, r, email)
	case http.MethodPost:
		if r.Header.Get("Origin") != s.origin {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		s.createWorkspace(w, r, email)
	}
}

func authenticatedEmail(r *http.Request) (string, bool) {
	email := strings.TrimSpace(r.Header.Get(IdentityEmailHeader))
	if email == "" {
		return "", false
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "", false
	}
	return strings.ToLower(email), true
}

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request, email string) {
	var workspaces workspacesv1alpha1.WorkspaceList
	if err := s.client.List(r.Context(), &workspaces, client.InNamespace(Namespace)); err != nil {
		log.Printf("list Workspaces for dashboard: %v", err)
		http.Error(w, "could not load workspaces", http.StatusInternalServerError)
		return
	}

	result := make([]workspaceSummary, 0)
	for i := range workspaces.Items {
		workspace := &workspaces.Items[i]
		if !strings.EqualFold(workspace.Spec.Owner, email) {
			continue
		}
		result = append(result, workspaceSummary{
			Name:  workspace.Name,
			Image: workspace.Spec.Image,
			Phase: workspace.Status.Phase,
			URL:   workspace.Status.URL,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

type createWorkspaceRequest struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type workspaceSummary struct {
	Name  string                            `json:"name"`
	Image string                            `json:"image,omitempty"`
	Phase workspacesv1alpha1.WorkspacePhase `json:"phase,omitempty"`
	URL   string                            `json:"url,omitempty"`
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request, email string) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var request createWorkspaceRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "request body must contain one JSON object", http.StatusBadRequest)
		return
	}
	if request.Name != strings.ToLower(request.Name) || !workspaceNamePattern.MatchString(request.Name) {
		http.Error(w, "name must be a DNS-compatible lowercase workspace name", http.StatusBadRequest)
		return
	}
	if request.Image == "" {
		request.Image = DefaultImage
	}
	if _, ok := allowedImages[request.Image]; !ok {
		http.Error(w, "image is not allowed", http.StatusBadRequest)
		return
	}

	workspace := &workspacesv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      request.Name,
			Namespace: Namespace,
		},
		Spec: workspacesv1alpha1.WorkspaceSpec{
			Owner: email,
			Image: request.Image,
			Port:  8080,
			Args:  []string{"--bind-addr", "0.0.0.0:8080", "--auth", "none"},
		},
	}
	if err := s.client.Create(r.Context(), workspace); err != nil {
		if apierrors.IsAlreadyExists(err) {
			http.Error(w, "a workspace with this name already exists", http.StatusConflict)
			return
		}
		log.Printf("create Workspace %s for dashboard: %v", request.Name, err)
		http.Error(w, "could not create workspace", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, workspaceSummary{
		Name:  workspace.Name,
		Image: workspace.Spec.Image,
		Phase: workspace.Status.Phase,
		URL:   workspace.Status.URL,
	})
}

func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email, ok := authenticatedEmail(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("Origin") != s.origin {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}

	name := r.PathValue("name")
	if !workspaceNamePattern.MatchString(name) {
		http.Error(w, "invalid workspace name", http.StatusBadRequest)
		return
	}
	workspace := &workspacesv1alpha1.Workspace{}
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: name, Namespace: Namespace}, workspace); err != nil {
		if apierrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		log.Printf("get Workspace %s for dashboard deletion: %v", name, err)
		http.Error(w, "could not load workspace", http.StatusInternalServerError)
		return
	}
	if !strings.EqualFold(workspace.Spec.Owner, email) {
		http.NotFound(w, r)
		return
	}
	if err := s.client.Delete(r.Context(), workspace); err != nil {
		if apierrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		log.Printf("delete Workspace %s for dashboard: %v", name, err)
		http.Error(w, "could not delete workspace", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write dashboard response: %v", err)
	}
}

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Workspaces</title>
  <style>
    :root { color-scheme: light; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; color: #172033; background: #f5f7fb; }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100vh; background: radial-gradient(ellipse at top left, #e9efff 0, transparent 38rem), #f5f7fb; }
    header { display: flex; justify-content: space-between; align-items: center; padding: 1rem max(1.25rem, calc((100% - 1040px) / 2)); background: rgba(255,255,255,.9); border-bottom: 1px solid #e4e9f2; }
    .brand { display: flex; align-items: center; gap: .7rem; font-weight: 750; letter-spacing: -.02em; }
    .brand-mark { display: grid; place-items: center; width: 2.2rem; height: 2.2rem; border-radius: .7rem; color: #fff; background: linear-gradient(135deg,#3468e8,#6848d6); }
    main { max-width: 1040px; margin: 3rem auto; padding: 0 1.25rem; }
    .intro { margin-bottom: 1.8rem; }
    .eyebrow { margin: 0 0 .45rem; color: #536fbd; font-size: .76rem; font-weight: 750; letter-spacing: .12em; text-transform: uppercase; }
    h1 { margin: 0; font-size: clamp(1.8rem,4vw,2.4rem); letter-spacing: -.045em; }
    .subtitle { margin: .55rem 0 0; color: #64728a; }
    .grid { display: grid; grid-template-columns: minmax(270px,.82fr) minmax(0,1.5fr); gap: 1.1rem; align-items: start; }
    section { background: rgba(255,255,255,.94); border: 1px solid #e3e8f1; border-radius: 16px; padding: 1.35rem; box-shadow: 0 12px 35px rgba(27,44,82,.055); }
    .section-heading { display: flex; justify-content: space-between; align-items: center; gap: .75rem; margin-bottom: 1.1rem; }
    h2 { font-size: 1rem; margin: 0; letter-spacing: -.015em; }
    form { display: grid; gap: .9rem; }
    label { display: grid; gap: .4rem; color: #46546b; font-size: .85rem; font-weight: 650; }
    input, select, button, .button { font: inherit; padding: .7rem .8rem; border: 1px solid #d4dce9; border-radius: 9px; }
    input, select { width: 100%; color: #172033; background: white; outline: none; }
    input:focus, select:focus { border-color: #6687eb; box-shadow: 0 0 0 3px rgba(76,112,225,.14); }
    button, .button { display: inline-flex; align-items: center; justify-content: center; gap: .4rem; color: #fff; background: #315ed4; border-color: #315ed4; font-weight: 700; text-decoration: none; cursor: pointer; transition: background .15s, transform .15s; }
    button:hover, .button:hover { background: #244ebf; transform: translateY(-1px); }
    button:disabled { opacity: .6; cursor: wait; transform: none; }
    .secondary { background: white; color: #34425a; border-color: #d4dce9; }
    .secondary:hover { background: #f4f6fb; }
    .danger { padding: .55rem .7rem; background: #fff; color: #b42332; border-color: #f0cbd0; font-size: .85rem; }
    .danger:hover { background: #fff4f4; }
    #workspace-list { display: grid; gap: .7rem; list-style: none; padding: 0; margin: 0; }
    .workspace-card { display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding: .9rem; border: 1px solid #e6eaf1; border-radius: 11px; background: #fff; }
    .workspace-info { min-width: 0; display: grid; gap: .35rem; }
    .workspace-name { overflow: hidden; color: #1c2940; font-weight: 720; text-overflow: ellipsis; }
    .workspace-meta { display: flex; align-items: center; gap: .45rem; color: #69768a; font-size: .8rem; }
    .badge { display: inline-flex; padding: .2rem .5rem; border-radius: 999px; background: #eef1f6; color: #58677e; font-size: .72rem; font-weight: 700; }
    .badge-running { background: #e7f7ee; color: #177245; }
    .actions { display: flex; align-items: center; gap: .5rem; flex-shrink: 0; }
    .actions .button { padding: .5rem .7rem; font-size: .85rem; }
    .muted { color: #7a8799; }
    #message { min-height: 1.25rem; margin: .8rem 0 0; color: #53627a; font-size: .85rem; }
    @media (max-width: 720px) { main { margin: 2rem auto; } .grid { grid-template-columns: 1fr; } }
    @media (max-width: 470px) { .workspace-card { align-items: flex-start; flex-direction: column; } .actions { width: 100%; justify-content: flex-end; } }
  </style>
</head>
<body>
  <header><div class="brand"><span class="brand-mark">W</span><span>Workspace Portal</span></div><a class="button secondary" href="https://authenticate.localhost.pomerium.io/.pomerium/sign_out?pomerium_redirect_uri=https%3A%2F%2Fauthenticate.localhost.pomerium.io">Log out</a></header>
  <main>
    <div class="intro"><p class="eyebrow">Your environment</p><h1>Your workspaces</h1><p class="subtitle">Create, open, and manage your personal development spaces.</p></div>
    <div class="grid">
    <section>
      <div class="section-heading"><h2>Create workspace</h2></div>
      <form id="create-form">
        <label>Workspace name<input name="name" required pattern="[a-z]([a-z0-9\-]{0,61}[a-z0-9])?" maxlength="63" placeholder="my-workspace"></label>
        <label>Workspace image<select name="image"><option value="codercom/code-server">codercom/code-server</option></select></label>
        <button type="submit">Create workspace</button>
      </form>
      <p id="message" role="status"></p>
    </section>
    <section>
      <div class="section-heading"><h2>Your environments</h2><button class="secondary" id="refresh-button" type="button">Refresh</button></div>
      <ul id="workspace-list"><li class="muted">Loading workspaces...</li></ul>
    </section>
    </div>
  </main>
  <script>
    const list = document.querySelector("#workspace-list");
    const message = document.querySelector("#message");
    async function loadWorkspaces() {
      const response = await fetch("/api/workspaces");
      if (!response.ok) throw new Error("Could not load workspaces.");
      const workspaces = await response.json();
      list.replaceChildren();
      if (!workspaces.length) {
        const empty = document.createElement("li");
        empty.className = "muted";
        empty.textContent = "No workspaces yet. Create one above.";
        list.append(empty);
        return;
      }
      for (const workspace of workspaces) {
        const item = document.createElement("li");
        item.className = "workspace-card";
        const details = document.createElement("div");
        details.className = "workspace-info";
        const name = document.createElement("span");
        name.className = "workspace-name";
        name.textContent = workspace.name;
        const meta = document.createElement("span");
        meta.className = "workspace-meta";
        const phase = document.createElement("span");
        phase.className = "badge" + (workspace.phase === "Running" ? " badge-running" : "");
        phase.textContent = workspace.phase || "Pending";
        const image = document.createElement("span");
        image.textContent = workspace.image || "codercom/code-server";
        meta.append(phase, image);
        details.append(name, meta);
        item.append(details);
        const actions = document.createElement("div");
        actions.className = "actions";
        let link;
        if (workspace.url && workspace.phase === "Running") {
          link = document.createElement("a");
          link.href = workspace.url;
          link.className = "button";
          link.textContent = "Open";
        }
        if (link) actions.append(link);
        const remove = document.createElement("button");
        remove.className = "danger";
        remove.type = "button";
        remove.textContent = "Delete";
        remove.addEventListener("click", () => deleteWorkspace(workspace.name, remove));
        actions.append(remove);
        item.append(actions);
        list.append(item);
      }
    }
    async function deleteWorkspace(name, button) {
      if (!confirm("Delete workspace \"" + name + "\"? Its running container will be stopped and removed.")) return;
      button.disabled = true;
      message.textContent = "Deleting " + name + "...";
      try {
        const response = await fetch("/api/workspaces/" + encodeURIComponent(name), { method: "DELETE" });
        if (!response.ok) throw new Error(await response.text());
        message.textContent = name + " is deleting.";
        await loadWorkspaces();
      } catch (error) {
        button.disabled = false;
        message.textContent = error.message || "Workspace deletion failed.";
      }
    }
    document.querySelector("#create-form").addEventListener("submit", async (event) => {
      event.preventDefault();
      const formElement = event.currentTarget;
      const submitButton = formElement.querySelector("button[type=submit]");
      submitButton.disabled = true;
      message.textContent = "Creating workspace...";
      const form = new FormData(formElement);
      try {
        const response = await fetch("/api/workspaces", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: form.get("name"), image: form.get("image") })
        });
        if (!response.ok) throw new Error(await response.text());
        message.textContent = "Workspace created. Refreshing your list...";
        formElement.reset();
        await loadWorkspaces();
        message.textContent = "Workspace created. It will appear as soon as it starts.";
      } catch (error) {
        message.textContent = error.message || "Workspace creation failed.";
      } finally {
        submitButton.disabled = false;
      }
    });
    document.querySelector("#refresh-button").addEventListener("click", async () => {
      message.textContent = "Refreshing workspaces...";
      try {
        await loadWorkspaces();
        message.textContent = "Workspace list is up to date.";
      } catch (error) {
        message.textContent = error.message;
      }
    });
    loadWorkspaces().catch((error) => { message.textContent = error.message; });
    window.setInterval(() => loadWorkspaces().catch((error) => { message.textContent = error.message; }), 10000);
  </script>
</body>
</html>`
