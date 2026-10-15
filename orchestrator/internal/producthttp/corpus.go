package producthttp

import (
	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) corpusCapture(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.CorpusCaptureInput](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid corpus capture")
		return
	}
	result, err := a.Store.CaptureCorpusEpisode(r.Context(), requestToken(r), r.PathValue("group"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) corpusSeal(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.CorpusSnapshotInput](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid corpus snapshot")
		return
	}
	result, err := a.Store.SealCorpusSnapshot(r.Context(), requestToken(r), r.PathValue("group"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) corpusContent(w http.ResponseWriter, r *http.Request) {
	raw, err := a.Store.CorpusSnapshotContent(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("snapshot"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=corpus.json")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}
func (a *Authentication) corpusFrontier(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.CorpusFrontier(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("binding"), r.PathValue("source"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
