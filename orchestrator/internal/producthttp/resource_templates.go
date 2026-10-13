package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) templateRoutes(mux *http.ServeMux) {
	a.environmentUpdateRoutes(mux)
	mux.HandleFunc("GET /api/me/project-configurations", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Store.UserProjectConfigurations(r.Context(), requestToken(r))
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"projects": out})
	})
	mux.HandleFunc("PATCH /api/me/project-configurations/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body product.ConfigurationEdit
		if !decode(w, r, 8<<10, &body) {
			return
		}
		out, err := a.Store.ChangeProjectNetwork(r.Context(), requestToken(r), r.PathValue("id"), body)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})

	mux.HandleFunc("GET /api/admin/shared-volumes", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Store.SharedVolumes(r.Context(), requestToken(r))
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"volumes": out})
	})
	saveVolume := func(w http.ResponseWriter, r *http.Request) {
		var body product.SharedVolumeEdit
		if !decode(w, r, 8<<10, &body) {
			return
		}
		out, err := a.Store.SaveSharedVolume(r.Context(), requestToken(r), r.PathValue("id"), body)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	}
	mux.HandleFunc("POST /api/admin/shared-volumes", saveVolume)
	mux.HandleFunc("PATCH /api/admin/shared-volumes/{id}", saveVolume)
	mux.HandleFunc("DELETE /api/admin/shared-volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body product.AccountRevision
		if !decode(w, r, 8<<10, &body) {
			return
		}
		out, err := a.Store.DeleteSharedVolume(r.Context(), requestToken(r), r.PathValue("id"), body)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})

	mux.HandleFunc("GET /api/admin/resource-templates/{kind}", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Store.ResourceTemplates(r.Context(), requestToken(r), r.PathValue("kind"))
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"templates": out})
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var body product.TemplateEdit
		if !decode(w, r, 256<<10, &body) {
			return
		}
		out, err := a.Store.SaveResourceTemplate(r.Context(), requestToken(r), r.PathValue("kind"), r.PathValue("id"), body)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	}
	mux.HandleFunc("POST /api/admin/resource-templates/{kind}", save)
	mux.HandleFunc("PATCH /api/admin/resource-templates/{kind}/{id}", save)
	mux.HandleFunc("DELETE /api/admin/resource-templates/{kind}/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body product.AccountRevision
		if !decode(w, r, 8<<10, &body) {
			return
		}
		out, err := a.Store.DeleteResourceTemplate(r.Context(), requestToken(r), r.PathValue("kind"), r.PathValue("id"), body)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/me/configuration", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Store.ConfigurationSettings(r.Context(), requestToken(r))
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("PATCH /api/me/configuration", func(w http.ResponseWriter, r *http.Request) {
		var body product.ConfigurationEdit
		if !decode(w, r, 8<<10, &body) {
			return
		}
		out, err := a.Store.SaveConfigurationPreferences(r.Context(), requestToken(r), body)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/admin/users/{id}/configuration", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Store.AdminConfigurationSettings(r.Context(), requestToken(r), r.PathValue("id"))
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	for _, kind := range []string{"user", "group"} {
		mux.HandleFunc("PUT /api/admin/"+kind+"s/{id}/configuration-defaults", func(w http.ResponseWriter, r *http.Request) {
			var body product.ConfigurationEdit
			if !decode(w, r, 8<<10, &body) {
				return
			}
			out, err := a.Store.SaveConfigurationDefaults(r.Context(), requestToken(r), kind, r.PathValue("id"), body)
			if err != nil {
				adminFailure(w, err)
				return
			}
			writeJSON(w, 200, out)
		})
	}
	mux.HandleFunc("GET /api/admin/policies/{id}/resources", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Store.PolicyResources(r.Context(), requestToken(r), r.PathValue("id"))
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"resources": out})
	})
	mux.HandleFunc("PUT /api/admin/policies/{id}/resources", func(w http.ResponseWriter, r *http.Request) {
		var body product.PolicyResourcesEdit
		if !decode(w, r, 128<<10, &body) {
			return
		}
		out, err := a.Store.SavePolicyResources(r.Context(), requestToken(r), r.PathValue("id"), body)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
}
