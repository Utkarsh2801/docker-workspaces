package controller

import (
	"context"

	"github.com/utkarsh2801/docker-workspaces/internal/compute"
)

type FakeProvider struct {
	Instances  map[string]*compute.Instance
	CreateErr  error
	DeleteErr  error
	GetErr     error
	ListErr    error
	Created    []compute.CreateRequest
	Deleted    []string
	ListResult []*compute.Instance
}

func (p *FakeProvider) Create(_ context.Context, req compute.CreateRequest) (*compute.Instance, error) {
	p.Created = append(p.Created, req)
	if p.CreateErr != nil {
		return nil, p.CreateErr
	}

	instance := &compute.Instance{
		ID:    "container-" + req.UID,
		UID:   req.UID,
		State: "creating",
	}
	if p.Instances == nil {
		p.Instances = make(map[string]*compute.Instance)
	}
	p.Instances[req.UID] = instance
	return instance, nil
}

func (p *FakeProvider) Delete(_ context.Context, uid string) error {
	p.Deleted = append(p.Deleted, uid)
	if p.DeleteErr != nil {
		return p.DeleteErr
	}
	delete(p.Instances, uid)
	return nil
}

func (p *FakeProvider) Get(_ context.Context, uid string) (*compute.Instance, error) {
	if p.GetErr != nil {
		return nil, p.GetErr
	}
	if instance, ok := p.Instances[uid]; ok {
		return instance, nil
	}
	return nil, compute.ErrNotFound
}

func (p *FakeProvider) List(context.Context) ([]*compute.Instance, error) {
	return p.ListResult, p.ListErr
}

var _ compute.Provider = (*FakeProvider)(nil)
