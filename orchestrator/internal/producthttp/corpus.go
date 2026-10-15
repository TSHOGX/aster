package producthttp

import (
	"aster/contracts/evolution"
	"errors"
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"strconv"
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

func (a *Authentication) corpusCollectors(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.CorpusCollectors(r.Context(), requestToken(r), r.PathValue("group"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *Authentication) evolutionTriggers(w http.ResponseWriter, r *http.Request) {
	var after int64
	if value := r.URL.Query().Get("after"); value != "" {
		var err error
		after, err = strconv.ParseInt(value, 10, 64)
		if err != nil || after < 0 {
			writeError(w, 400, "invalid trigger cursor")
			return
		}
	}
	result, err := a.Store.EvolutionTriggers(r.Context(), requestToken(r), r.PathValue("group"), after)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *Authentication) backfillPreview(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.EvolutionBackfillInput](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid backfill preview")
		return
	}
	result, err := a.Store.PreviewEvolutionBackfill(r.Context(), requestToken(r), r.PathValue("group"), input)
	if errors.Is(err, product.ErrCapacity) {
		writeJSON(w, 422, map[string]string{"code": "backfill_limit_exceeded", "error": "selected evidence exceeds the episode or input-size budget; narrow the range or adjust its bounded limit"})
		return
	}

	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) backfillRead(w http.ResponseWriter, r *http.Request) {
	result, err := a.Store.EvolutionBackfill(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("backfill"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, result)
}
func (a *Authentication) backfillCommand(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.BackfillCommand](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid backfill command")
		return
	}
	result, err := a.Store.CommandEvolutionBackfill(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("backfill"), r.PathValue("action"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *Authentication) backfillContent(w http.ResponseWriter, r *http.Request) {
	ref, raw, err := a.Store.BackfillInputContent(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("backfill"), r.PathValue("binding"), r.PathValue("port"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=backfill.json")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("ETag", `"`+ref.Digest+`"`)
	_, _ = w.Write(raw)
}

func (a *Authentication) backfillStatus(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.EvolutionBackfillStatus(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("backfill"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, value)
}
