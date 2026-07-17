package handlers

import (
	"net/http"
)

func (h *Handler) OPDSRoot(w http.ResponseWriter, r *http.Request) {
	h.opdsCatalog.ServeRoot(w, r)
}

func (h *Handler) OPDSCover(w http.ResponseWriter, r *http.Request) {
	h.opdsCatalog.ServeCover(w, r)
}

func (h *Handler) OPDSEpub(w http.ResponseWriter, r *http.Request) {
	h.opdsCatalog.ServeEPUB(w, r)
}

func (h *Handler) OPDSImage(w http.ResponseWriter, r *http.Request) {
	h.opdsCatalog.ServeImage(w, r)
}

func (h *Handler) OPDSComicCBZ(w http.ResponseWriter, r *http.Request) {
	h.opdsCatalog.ServeComicCBZ(w, r)
}
