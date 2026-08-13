package server

import (
	"context"
	"net/http"
)

type automationToolCatalogService struct {
	catalog func() *AutomationToolCatalog
}

func (s *Server) automationTools() automationToolCatalogService {
	var catalog func() *AutomationToolCatalog
	if s != nil {
		catalog = s.automationToolCatalogSnapshot
	}
	return automationToolCatalogService{catalog: catalog}
}

func (a automationToolCatalogService) snapshot() *AutomationToolCatalog {
	if a.catalog == nil {
		return nil
	}
	return a.catalog()
}

func (a automationToolCatalogService) list(ctx context.Context) ([]AutomationToolCatalogItem, error) {
	catalog := a.snapshot()
	if catalog == nil {
		return nil, apiErr(http.StatusServiceUnavailable, "optional automation tool catalog is unavailable")
	}
	items, err := catalog.List(ctx)
	if err != nil {
		return nil, apiErr(http.StatusInternalServerError, err.Error())
	}
	return items, nil
}

func (a automationToolCatalogService) install(ctx context.Context, id string) (AutomationToolCatalogItem, error) {
	catalog := a.snapshot()
	if catalog == nil {
		return AutomationToolCatalogItem{}, apiErr(http.StatusServiceUnavailable, "optional automation tool catalog is unavailable")
	}
	item, err := catalog.Install(ctx, id)
	if err != nil {
		return AutomationToolCatalogItem{}, apiErr(automationToolCatalogErrorStatus(err), err.Error())
	}
	return item, nil
}

func (a automationToolCatalogService) configure(ctx context.Context, id string) (AutomationToolCatalogItem, error) {
	catalog := a.snapshot()
	if catalog == nil {
		return AutomationToolCatalogItem{}, apiErr(http.StatusServiceUnavailable, "optional automation tool catalog is unavailable")
	}
	item, err := catalog.Configure(ctx, id)
	if err != nil {
		return AutomationToolCatalogItem{}, apiErr(automationToolCatalogErrorStatus(err), err.Error())
	}
	return item, nil
}
