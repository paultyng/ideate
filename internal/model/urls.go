package model

// IdeateURLScheme is the custom URL scheme used by deep-links that
// agents, skills, and orchestrator prompts emit so the user can click
// to navigate inside Ideate. In-process clicks (inside Ideate's own
// xterm terminals or markdown views) route via the frontend
// `handleLink` dispatcher (see `frontend/src/lib/deeplink.ts`); the
// OS-level scheme handler (`open ideate://...` from a shell or other
// app) is wired via Wails' `Mac.OnUrlOpen` callback and forwards the
// URL to the same dispatcher.
//
// Path grammar:
//
//	ideate://orchestrator                            — orchestrator drawer (singleton).
//	ideate://ideas/<slug>                            — idea detail.
//	ideate://ideas/<slug>/active-session             — synthetic: running session if one
//	                                                   exists; else resume the most-recent
//	                                                   dormant; else fall through to the
//	                                                   idea page.
//	ideate://ideas/<slug>/sessions/<uuid>            — specific session permalink.
//
// "ideas" and "sessions" are intentionally plural — matches REST
// convention and lets the path read as "session uuid under the
// sessions collection of the bar idea." The orchestrator stays
// outside `ideas/` because it isn't one.
const IdeateURLScheme = "ideate"

// OrchestratorURL returns the deep-link that opens the orchestrator
// drawer. The orchestrator is a singleton — no slug or uuid.
func OrchestratorURL() string {
	return IdeateURLScheme + "://orchestrator"
}

// IdeaURL returns the deep-link for an idea's detail surface.
func IdeaURL(slug string) string {
	if slug == "" {
		return ""
	}
	return IdeateURLScheme + "://ideas/" + slug
}

// IdeaActiveSessionURL returns the synthetic deep-link that opens
// the idea's "currently active" session. On click the handler:
//
//  1. If a running session exists, navigates into it.
//  2. Else if a dormant session exists, resumes it then navigates.
//  3. Else falls back to the idea detail page.
//
// Encodes the "switch to X" semantics the quick switcher already
// implements, but as a stable URL that skills can emit ahead of
// time without knowing the session state at render time.
func IdeaActiveSessionURL(slug string) string {
	if slug == "" {
		return ""
	}
	return IdeateURLScheme + "://ideas/" + slug + "/active-session"
}

// SessionURL returns the permalink deep-link for a specific session
// by uuid. Used when a tool / skill needs to point at one session
// in particular (audit trails, "this work happened in <session>").
// For "send me to whichever session is live", emit
// IdeaActiveSessionURL instead.
func SessionURL(slug, uuid string) string {
	if slug == "" || uuid == "" {
		return ""
	}
	return IdeateURLScheme + "://ideas/" + slug + "/sessions/" + uuid
}
