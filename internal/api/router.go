package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prodesire/cloud-steward/internal/governance"
	"github.com/prodesire/cloud-steward/internal/store"
)

func NewRouter(repo store.Repository) http.Handler {
	return NewRouterWithExecutor(repo, nil)
}

func NewRouterWithExecutor(repo store.Repository, executor governance.PlanExecutor) http.Handler {
	h := &handler{repo: repo, governance: governance.NewServiceWithExecutor(repo, executor)}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Post("/scans", h.createScan)
		r.Get("/scans", h.listScans)
		r.Get("/scans/{id}", h.getScan)
		r.Post("/scans/{id}/reconcile", h.reconcileScan)
		r.Get("/resources", h.listResources)
		r.Get("/resources/{id}", h.getResource)
		r.Get("/graph", h.listGraph)
		r.Get("/candidates", h.listCandidates)
		r.Patch("/candidates/{id}", h.updateCandidate)
		r.Post("/plans", h.createPlan)
		r.Get("/plans", h.listPlans)
		r.Get("/plans/{id}", h.getPlan)
		r.Get("/plans/{id}/export", h.exportPlan)
		r.Post("/plans/{id}/approve", h.approvePlan)
		r.Post("/plans/{id}/execute", h.executePlan)
		r.Get("/audits/export", h.exportAudits)
		r.Get("/audits", h.listAudits)
		r.Get("/reports/savings", h.savingsReport)
	})
	return r
}
