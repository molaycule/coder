package coderd

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/exp/slices"
	"golang.org/x/xerrors"

	"github.com/coder/coder/coderd/database"
	"github.com/coder/coder/coderd/httpapi"
	"github.com/coder/coder/coderd/httpmw"
	"github.com/coder/coder/coderd/rbac"
	"github.com/coder/coder/codersdk"
)

func (api *API) workspaceBuild(rw http.ResponseWriter, r *http.Request) {
	workspaceBuild := httpmw.WorkspaceBuildParam(r)
	workspace := httpmw.WorkspaceParam(r)

	if !api.Authorize(r, rbac.ActionRead, workspace) {
		httpapi.ResourceNotFound(rw)
		return
	}

	job, err := api.Database.GetProvisionerJobByID(r.Context(), workspaceBuild.JobID)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching provisioner job.",
			Detail:  err.Error(),
		})
		return
	}

	users, err := api.Database.GetUsersByIDs(r.Context(), database.GetUsersByIDsParams{
		IDs: []uuid.UUID{workspace.OwnerID, workspaceBuild.InitiatorID},
	})
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching user.",
			Detail:  err.Error(),
		})
		return
	}

	workspaceResources, err := api.Database.GetWorkspaceResourcesByJobIDs(r.Context(), []uuid.UUID{job.ID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace resources.",
			Detail:  err.Error(),
		})
		return
	}

	resourceIDs := make([]uuid.UUID, 0)
	for _, resource := range workspaceResources {
		resourceIDs = append(resourceIDs, resource.ID)
	}

	resourceMetadata, err := api.Database.GetWorkspaceResourceMetadataByResourceIDs(r.Context(), resourceIDs)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace resource metadata.",
			Detail:  err.Error(),
		})
		return
	}

	resourceAgents, err := api.Database.GetWorkspaceAgentsByResourceIDs(r.Context(), resourceIDs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace resource agents.",
			Detail:  err.Error(),
		})
		return
	}

	resourceAgentIDs := make([]uuid.UUID, 0)
	for _, agent := range resourceAgents {
		resourceAgentIDs = append(resourceAgentIDs, agent.ID)
	}

	agentApps, err := api.Database.GetWorkspaceAppsByAgentIDs(r.Context(), resourceAgentIDs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace apps.",
			Detail:  err.Error(),
		})
		return
	}

	wsb, err := api.convertWorkspaceBuilds(
		[]database.WorkspaceBuild{workspaceBuild},
		[]database.Workspace{workspace},
		users,
		[]database.ProvisionerJob{job},
		workspaceResources,
		resourceMetadata,
		resourceAgents,
		agentApps,
	)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error converting workspace build.",
			Detail:  err.Error(),
		})
		return
	}

	httpapi.Write(rw, http.StatusOK, wsb)
}

func (api *API) workspaceBuilds(rw http.ResponseWriter, r *http.Request) {
	workspace := httpmw.WorkspaceParam(r)

	if !api.Authorize(r, rbac.ActionRead, workspace) {
		httpapi.ResourceNotFound(rw)
		return
	}

	paginationParams, ok := parsePagination(rw, r)
	if !ok {
		return
	}

	var workspaceBuilds []database.WorkspaceBuild
	// Ensure all db calls happen in the same tx
	err := api.Database.InTx(func(store database.Store) error {
		var err error
		if paginationParams.AfterID != uuid.Nil {
			// See if the record exists first. If the record does not exist, the pagination
			// query will not work.
			_, err := store.GetWorkspaceBuildByID(r.Context(), paginationParams.AfterID)
			if err != nil && xerrors.Is(err, sql.ErrNoRows) {
				httpapi.Write(rw, http.StatusBadRequest, codersdk.Response{
					Message: fmt.Sprintf("Record at \"after_id\" (%q) does not exist.", paginationParams.AfterID.String()),
				})
				return err
			} else if err != nil {
				httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
					Message: "Internal error fetching workspace build at \"after_id\".",
					Detail:  err.Error(),
				})
				return err
			}
		}

		req := database.GetWorkspaceBuildByWorkspaceIDParams{
			WorkspaceID: workspace.ID,
			AfterID:     paginationParams.AfterID,
			OffsetOpt:   int32(paginationParams.Offset),
			LimitOpt:    int32(paginationParams.Limit),
		}
		workspaceBuilds, err = store.GetWorkspaceBuildByWorkspaceID(r.Context(), req)
		if xerrors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		if err != nil {
			httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
				Message: "Internal error fetching workspace build.",
				Detail:  err.Error(),
			})
			return err
		}

		return nil
	})
	if err != nil {
		return
	}

	userIDs := make([]uuid.UUID, 0, len(workspaceBuilds))
	for _, build := range workspaceBuilds {
		userIDs = append(userIDs, build.InitiatorID)
	}
	users, err := api.Database.GetUsersByIDs(r.Context(), database.GetUsersByIDsParams{
		IDs: userIDs,
	})

	jobIDs := make([]uuid.UUID, 0, len(workspaceBuilds))
	for _, build := range workspaceBuilds {
		jobIDs = append(jobIDs, build.JobID)
	}
	jobs, err := api.Database.GetProvisionerJobsByIDs(r.Context(), jobIDs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace jobs.",
			Detail:  err.Error(),
		})
		return
	}

	jobByID := map[uuid.UUID]database.ProvisionerJob{}
	for _, job := range jobs {
		jobByID[job.ID] = job
	}

	workspaceResources, err := api.Database.GetWorkspaceResourcesByJobIDs(r.Context(), jobIDs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace resources.",
			Detail:  err.Error(),
		})
		return
	}

	resourceIDs := make([]uuid.UUID, 0)
	for _, resource := range workspaceResources {
		resourceIDs = append(resourceIDs, resource.ID)
	}

	resourceMetadata, err := api.Database.GetWorkspaceResourceMetadataByResourceIDs(r.Context(), resourceIDs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace resource metadata.",
			Detail:  err.Error(),
		})
		return
	}

	resourceAgents, err := api.Database.GetWorkspaceAgentsByResourceIDs(r.Context(), resourceIDs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace agents.",
			Detail:  err.Error(),
		})
		return
	}

	resourceAgentIDs := make([]uuid.UUID, 0)
	for _, agent := range resourceAgents {
		resourceAgentIDs = append(resourceAgentIDs, agent.ID)
	}

	agentApps, err := api.Database.GetWorkspaceAppsByAgentIDs(r.Context(), resourceAgentIDs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace apps.",
			Detail:  err.Error(),
		})
		return
	}

	apiBuilds, err := api.convertWorkspaceBuilds(
		workspaceBuilds,
		[]database.Workspace{workspace},
		users,
		jobs,
		workspaceResources,
		resourceMetadata,
		resourceAgents,
		agentApps,
	)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error converting workspace build.",
			Detail:  err.Error(),
		})
		return
	}

	httpapi.Write(rw, http.StatusOK, apiBuilds)
}

func (api *API) workspaceBuildByBuildNumber(rw http.ResponseWriter, r *http.Request) {
	owner := httpmw.UserParam(r)
	workspaceName := chi.URLParam(r, "workspacename")
	buildNumber, err := strconv.ParseInt(chi.URLParam(r, "buildnumber"), 10, 32)
	if err != nil {
		httpapi.Write(rw, http.StatusBadRequest, codersdk.Response{
			Message: "Failed to parse build number as integer.",
			Detail:  err.Error(),
		})
		return
	}

	workspace, err := api.Database.GetWorkspaceByOwnerIDAndName(r.Context(), database.GetWorkspaceByOwnerIDAndNameParams{
		OwnerID: owner.ID,
		Name:    workspaceName,
	})
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.ResourceNotFound(rw)
		return
	}
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace by name.",
			Detail:  err.Error(),
		})
		return
	}

	if !api.Authorize(r, rbac.ActionRead, workspace) {
		httpapi.ResourceNotFound(rw)
		return
	}

	workspaceBuild, err := api.Database.GetWorkspaceBuildByWorkspaceIDAndBuildNumber(r.Context(), database.GetWorkspaceBuildByWorkspaceIDAndBuildNumberParams{
		WorkspaceID: workspace.ID,
		BuildNumber: int32(buildNumber),
	})
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusNotFound, codersdk.Response{
			Message: fmt.Sprintf("Workspace %q Build %d does not exist.", workspaceName, buildNumber),
		})
		return
	}
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace build.",
			Detail:  err.Error(),
		})
		return
	}

	initiator, err := api.Database.GetUserByID(r.Context(), workspaceBuild.InitiatorID)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace build initiator.",
			Detail:  err.Error(),
		})
		return
	}

	job, err := api.Database.GetProvisionerJobByID(r.Context(), workspaceBuild.JobID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace jobs.",
			Detail:  err.Error(),
		})
		return
	}

	workspaceResources, err := api.Database.GetWorkspaceResourcesByJobID(r.Context(), job.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace resources.",
			Detail:  err.Error(),
		})
		return
	}

	resourceIDs := make([]uuid.UUID, 0)
	for _, resource := range workspaceResources {
		resourceIDs = append(resourceIDs, resource.ID)
	}

	resourceMetadata, err := api.Database.GetWorkspaceResourceMetadataByResourceIDs(r.Context(), resourceIDs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace resource metadata.",
			Detail:  err.Error(),
		})
		return
	}

	resourceAgents, err := api.Database.GetWorkspaceAgentsByResourceIDs(r.Context(), resourceIDs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace agents.",
			Detail:  err.Error(),
		})
		return
	}

	resourceAgentIDs := make([]uuid.UUID, 0)
	for _, agent := range resourceAgents {
		resourceAgentIDs = append(resourceAgentIDs, agent.ID)
	}

	agentApps, err := api.Database.GetWorkspaceAppsByAgentIDs(r.Context(), resourceAgentIDs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching workspace apps.",
			Detail:  err.Error(),
		})
		return
	}

	wsb, err := api.convertWorkspaceBuilds(
		[]database.WorkspaceBuild{workspaceBuild},
		[]database.Workspace{workspace},
		[]database.User{owner, initiator},
		[]database.ProvisionerJob{job},
		workspaceResources,
		resourceMetadata,
		resourceAgents,
		agentApps,
	)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error converting workspace build.",
			Detail:  err.Error(),
		})
		return
	}

	httpapi.Write(rw, http.StatusOK, wsb)
}

func (api *API) postWorkspaceBuilds(rw http.ResponseWriter, r *http.Request) {
	apiKey := httpmw.APIKey(r)
	workspace := httpmw.WorkspaceParam(r)
	var createBuild codersdk.CreateWorkspaceBuildRequest
	if !httpapi.Read(rw, r, &createBuild) {
		return
	}

	// Rbac action depends on the transition
	var action rbac.Action
	switch createBuild.Transition {
	case codersdk.WorkspaceTransitionDelete:
		action = rbac.ActionDelete
	case codersdk.WorkspaceTransitionStart, codersdk.WorkspaceTransitionStop:
		action = rbac.ActionUpdate
	default:
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: fmt.Sprintf("Transition %q not supported.", createBuild.Transition),
		})
		return
	}
	if !api.Authorize(r, action, workspace) {
		httpapi.ResourceNotFound(rw)
		return
	}

	if createBuild.TemplateVersionID == uuid.Nil {
		latestBuild, err := api.Database.GetLatestWorkspaceBuildByWorkspaceID(r.Context(), workspace.ID)
		if err != nil {
			httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
				Message: "Internal error fetching the latest workspace build.",
				Detail:  err.Error(),
			})
			return
		}
		createBuild.TemplateVersionID = latestBuild.TemplateVersionID
	}

	templateVersion, err := api.Database.GetTemplateVersionByID(r.Context(), createBuild.TemplateVersionID)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusBadRequest, codersdk.Response{
			Message: "Template version not found.",
			Validations: []codersdk.ValidationError{{
				Field:  "template_version_id",
				Detail: "template version not found",
			}},
		})
		return
	}
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching template version.",
			Detail:  err.Error(),
		})
		return
	}

	template, err := api.Database.GetTemplateByID(r.Context(), templateVersion.TemplateID.UUID)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Failed to get template",
			Detail:  err.Error(),
		})
		return
	}

	var state []byte
	// If custom state, deny request since user could be corrupting or leaking
	// cloud state.
	if createBuild.ProvisionerState != nil || createBuild.Orphan {
		if !api.Authorize(r, rbac.ActionUpdate, template.RBACObject()) {
			httpapi.Write(rw, http.StatusForbidden, codersdk.Response{
				Message: "Only template managers may provide custom state",
			})
			return
		}
		state = createBuild.ProvisionerState
	}

	if createBuild.Orphan {
		if createBuild.Transition != codersdk.WorkspaceTransitionDelete {
			httpapi.Write(rw, http.StatusBadRequest, codersdk.Response{
				Message: "Orphan is only permitted when deleting a workspace.",
				Detail:  err.Error(),
			})
			return
		}

		if createBuild.ProvisionerState != nil && createBuild.Orphan {
			httpapi.Write(rw, http.StatusBadRequest, codersdk.Response{
				Message: "ProvisionerState cannot be set alongside Orphan since state intent is unclear.",
			})
			return
		}
		state = []byte{}
	}

	templateVersionJob, err := api.Database.GetProvisionerJobByID(r.Context(), templateVersion.JobID)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching provisioner job.",
			Detail:  err.Error(),
		})
		return
	}
	templateVersionJobStatus := convertProvisionerJob(templateVersionJob).Status
	switch templateVersionJobStatus {
	case codersdk.ProvisionerJobPending, codersdk.ProvisionerJobRunning:
		httpapi.Write(rw, http.StatusNotAcceptable, codersdk.Response{
			Message: fmt.Sprintf("The provided template version is %s. Wait for it to complete importing!", templateVersionJobStatus),
		})
		return
	case codersdk.ProvisionerJobFailed:
		httpapi.Write(rw, http.StatusPreconditionFailed, codersdk.Response{
			Message: fmt.Sprintf("The provided template version %q has failed to import: %q. You cannot build workspaces with it!", templateVersion.Name, templateVersionJob.Error.String),
		})
		return
	case codersdk.ProvisionerJobCanceled:
		httpapi.Write(rw, http.StatusPreconditionFailed, codersdk.Response{
			Message: "The provided template version was canceled during import. You cannot builds workspaces with it!",
		})
		return
	}

	// Store prior build number to compute new build number
	var priorBuildNum int32
	priorHistory, err := api.Database.GetLatestWorkspaceBuildByWorkspaceID(r.Context(), workspace.ID)
	if err == nil {
		priorJob, err := api.Database.GetProvisionerJobByID(r.Context(), priorHistory.JobID)
		if err == nil && convertProvisionerJob(priorJob).Status.Active() {
			httpapi.Write(rw, http.StatusConflict, codersdk.Response{
				Message: "A workspace build is already active.",
			})
			return
		}

		priorBuildNum = priorHistory.BuildNumber
	} else if !errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching prior workspace build.",
			Detail:  err.Error(),
		})
		return
	}

	if state == nil {
		state = priorHistory.ProvisionerState
	}

	var workspaceBuild database.WorkspaceBuild
	var provisionerJob database.ProvisionerJob
	// This must happen in a transaction to ensure history can be inserted, and
	// the prior history can update it's "after" column to point at the new.
	err = api.Database.InTx(func(db database.Store) error {
		existing, err := db.ParameterValues(r.Context(), database.ParameterValuesParams{
			Scopes:   []database.ParameterScope{database.ParameterScopeWorkspace},
			ScopeIds: []uuid.UUID{workspace.ID},
		})
		if err != nil && !xerrors.Is(err, sql.ErrNoRows) {
			return xerrors.Errorf("Fetch previous parameters: %w", err)
		}

		// Write/Update any new params
		now := database.Now()
		for _, param := range createBuild.ParameterValues {
			for _, exists := range existing {
				// If the param exists, delete the old param before inserting the new one
				if exists.Name == param.Name {
					err = db.DeleteParameterValueByID(r.Context(), exists.ID)
					if err != nil && !xerrors.Is(err, sql.ErrNoRows) {
						return xerrors.Errorf("Failed to delete old param %q: %w", exists.Name, err)
					}
				}
			}

			_, err = db.InsertParameterValue(r.Context(), database.InsertParameterValueParams{
				ID:                uuid.New(),
				Name:              param.Name,
				CreatedAt:         now,
				UpdatedAt:         now,
				Scope:             database.ParameterScopeWorkspace,
				ScopeID:           workspace.ID,
				SourceScheme:      database.ParameterSourceScheme(param.SourceScheme),
				SourceValue:       param.SourceValue,
				DestinationScheme: database.ParameterDestinationScheme(param.DestinationScheme),
			})
			if err != nil {
				return xerrors.Errorf("insert parameter value: %w", err)
			}
		}

		workspaceBuildID := uuid.New()
		input, err := json.Marshal(workspaceProvisionJob{
			WorkspaceBuildID: workspaceBuildID,
		})
		if err != nil {
			return xerrors.Errorf("marshal provision job: %w", err)
		}
		provisionerJob, err = db.InsertProvisionerJob(r.Context(), database.InsertProvisionerJobParams{
			ID:             uuid.New(),
			CreatedAt:      database.Now(),
			UpdatedAt:      database.Now(),
			InitiatorID:    apiKey.UserID,
			OrganizationID: template.OrganizationID,
			Provisioner:    template.Provisioner,
			Type:           database.ProvisionerJobTypeWorkspaceBuild,
			StorageMethod:  templateVersionJob.StorageMethod,
			StorageSource:  templateVersionJob.StorageSource,
			Input:          input,
		})
		if err != nil {
			return xerrors.Errorf("insert provisioner job: %w", err)
		}

		workspaceBuild, err = db.InsertWorkspaceBuild(r.Context(), database.InsertWorkspaceBuildParams{
			ID:                workspaceBuildID,
			CreatedAt:         database.Now(),
			UpdatedAt:         database.Now(),
			WorkspaceID:       workspace.ID,
			TemplateVersionID: templateVersion.ID,
			BuildNumber:       priorBuildNum + 1,
			ProvisionerState:  state,
			InitiatorID:       apiKey.UserID,
			Transition:        database.WorkspaceTransition(createBuild.Transition),
			JobID:             provisionerJob.ID,
			Reason:            database.BuildReasonInitiator,
		})
		if err != nil {
			return xerrors.Errorf("insert workspace build: %w", err)
		}

		return nil
	})
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error inserting workspace build.",
			Detail:  err.Error(),
		})
		return
	}

	users, err := api.Database.GetUsersByIDs(r.Context(), database.GetUsersByIDsParams{
		IDs: []uuid.UUID{
			workspace.OwnerID,
			workspaceBuild.InitiatorID,
		},
	})
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error getting user.",
			Detail:  err.Error(),
		})
		return
	}

	wsb, err := api.convertWorkspaceBuilds(
		[]database.WorkspaceBuild{workspaceBuild},
		[]database.Workspace{workspace},
		users,
		[]database.ProvisionerJob{provisionerJob},
		[]database.WorkspaceResource{},
		[]database.WorkspaceResourceMetadatum{},
		[]database.WorkspaceAgent{},
		[]database.WorkspaceApp{},
	)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error converting workspace build.",
			Detail:  err.Error(),
		})
		return
	}

	httpapi.Write(rw, http.StatusCreated, wsb)
}

func (api *API) patchCancelWorkspaceBuild(rw http.ResponseWriter, r *http.Request) {
	workspaceBuild := httpmw.WorkspaceBuildParam(r)
	workspace, err := api.Database.GetWorkspaceByID(r.Context(), workspaceBuild.WorkspaceID)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "No workspace exists for this job.",
		})
		return
	}

	if !api.Authorize(r, rbac.ActionUpdate, workspace) {
		httpapi.ResourceNotFound(rw)
		return
	}

	job, err := api.Database.GetProvisionerJobByID(r.Context(), workspaceBuild.JobID)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching provisioner job.",
			Detail:  err.Error(),
		})
		return
	}
	if job.CompletedAt.Valid {
		httpapi.Write(rw, http.StatusPreconditionFailed, codersdk.Response{
			Message: "Job has already completed!",
		})
		return
	}
	if job.CanceledAt.Valid {
		httpapi.Write(rw, http.StatusPreconditionFailed, codersdk.Response{
			Message: "Job has already been marked as canceled!",
		})
		return
	}
	err = api.Database.UpdateProvisionerJobWithCancelByID(r.Context(), database.UpdateProvisionerJobWithCancelByIDParams{
		ID: job.ID,
		CanceledAt: sql.NullTime{
			Time:  database.Now(),
			Valid: true,
		},
	})
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error updating provisioner job.",
			Detail:  err.Error(),
		})
		return
	}
	httpapi.Write(rw, http.StatusOK, codersdk.Response{
		Message: "Job has been marked as canceled...",
	})
}

func (api *API) workspaceBuildResources(rw http.ResponseWriter, r *http.Request) {
	workspaceBuild := httpmw.WorkspaceBuildParam(r)
	workspace, err := api.Database.GetWorkspaceByID(r.Context(), workspaceBuild.WorkspaceID)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "No workspace exists for this job.",
		})
		return
	}

	if !api.Authorize(r, rbac.ActionRead, workspace) {
		httpapi.ResourceNotFound(rw)
		return
	}

	job, err := api.Database.GetProvisionerJobByID(r.Context(), workspaceBuild.JobID)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching provisioner job.",
			Detail:  err.Error(),
		})
		return
	}
	api.provisionerJobResources(rw, r, job)
}

func (api *API) workspaceBuildLogs(rw http.ResponseWriter, r *http.Request) {
	workspaceBuild := httpmw.WorkspaceBuildParam(r)
	workspace, err := api.Database.GetWorkspaceByID(r.Context(), workspaceBuild.WorkspaceID)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "No workspace exists for this job.",
		})
		return
	}

	if !api.Authorize(r, rbac.ActionRead, workspace) {
		httpapi.ResourceNotFound(rw)
		return
	}

	job, err := api.Database.GetProvisionerJobByID(r.Context(), workspaceBuild.JobID)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching provisioner job.",
			Detail:  err.Error(),
		})
		return
	}
	api.provisionerJobLogs(rw, r, job)
}

func (api *API) workspaceBuildState(rw http.ResponseWriter, r *http.Request) {
	workspaceBuild := httpmw.WorkspaceBuildParam(r)
	workspace, err := api.Database.GetWorkspaceByID(r.Context(), workspaceBuild.WorkspaceID)
	if err != nil {
		httpapi.Write(rw, http.StatusInternalServerError, codersdk.Response{
			Message: "No workspace exists for this job.",
		})
		return
	}

	if !api.Authorize(r, rbac.ActionRead, workspace) {
		httpapi.ResourceNotFound(rw)
		return
	}

	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(workspaceBuild.ProvisionerState)
}

func convertWorkspaceBuild(
	workspaceOwner *database.User,
	buildInitiator *database.User,
	workspace database.Workspace,
	workspaceBuild database.WorkspaceBuild,
	job database.ProvisionerJob,
) codersdk.WorkspaceBuild {
	//nolint:unconvert
	if workspace.ID != workspaceBuild.WorkspaceID {
		panic("workspace and build do not match")
	}

	// Both owner and initiator should always be present. But from a static
	// code analysis POV, these could be nil.
	ownerName := "unknown"
	if workspaceOwner != nil {
		ownerName = workspaceOwner.Username
	}

	initiatorName := "unknown"
	if workspaceOwner != nil {
		initiatorName = buildInitiator.Username
	}

	return codersdk.WorkspaceBuild{
		ID:                 workspaceBuild.ID,
		CreatedAt:          workspaceBuild.CreatedAt,
		UpdatedAt:          workspaceBuild.UpdatedAt,
		WorkspaceOwnerID:   workspace.OwnerID,
		WorkspaceOwnerName: ownerName,
		WorkspaceID:        workspaceBuild.WorkspaceID,
		WorkspaceName:      workspace.Name,
		TemplateVersionID:  workspaceBuild.TemplateVersionID,
		BuildNumber:        workspaceBuild.BuildNumber,
		Transition:         codersdk.WorkspaceTransition(workspaceBuild.Transition),
		InitiatorID:        workspaceBuild.InitiatorID,
		InitiatorUsername:  initiatorName,
		Job:                convertProvisionerJob(job),
		Deadline:           codersdk.NewNullTime(workspaceBuild.Deadline, !workspaceBuild.Deadline.IsZero()),
		Reason:             codersdk.BuildReason(workspaceBuild.Reason),
		Resources:          []codersdk.WorkspaceResource{},
	}
}

func (api *API) convertWorkspaceBuilds(
	workspaceBuilds []database.WorkspaceBuild,
	workspaces []database.Workspace,
	users []database.User,
	jobs []database.ProvisionerJob,
	workspaceResources []database.WorkspaceResource,
	resourceMetadata []database.WorkspaceResourceMetadatum,
	resourceAgents []database.WorkspaceAgent,
	agentApps []database.WorkspaceApp,
) ([]codersdk.WorkspaceBuild, error) {

	workspaceByID := map[uuid.UUID]database.Workspace{}
	for _, workspace := range workspaces {
		workspaceByID[workspace.ID] = workspace
	}
	userByID := map[uuid.UUID]database.User{}
	for _, user := range users {
		userByID[user.ID] = user
	}
	jobByID := map[uuid.UUID]database.ProvisionerJob{}
	for _, job := range jobs {
		jobByID[job.ID] = job
	}
	resourcesByJobID := map[uuid.UUID][]database.WorkspaceResource{}
	for _, resource := range workspaceResources {
		resourcesByJobID[resource.JobID] = append(resourcesByJobID[resource.JobID], resource)
	}
	metadataByResourceID := map[uuid.UUID][]database.WorkspaceResourceMetadatum{}
	for _, metadata := range resourceMetadata {
		metadataByResourceID[metadata.WorkspaceResourceID] = append(metadataByResourceID[metadata.WorkspaceResourceID], metadata)
	}
	agentsByResourceID := map[uuid.UUID][]database.WorkspaceAgent{}
	for _, agent := range resourceAgents {
		agentsByResourceID[agent.ResourceID] = append(agentsByResourceID[agent.ResourceID], agent)
	}
	appsByAgentID := map[uuid.UUID][]database.WorkspaceApp{}
	for _, app := range agentApps {
		appsByAgentID[app.AgentID] = append(appsByAgentID[app.AgentID], app)
	}

	var apiBuilds []codersdk.WorkspaceBuild
	for _, build := range workspaceBuilds {
		job, exists := jobByID[build.JobID]
		if !exists {
			return nil, xerrors.New("build job not found")
		}
		workspace, exists := workspaceByID[build.WorkspaceID]
		if !exists {
			return nil, xerrors.New("workspace not found")
		}
		owner, exists := userByID[workspace.OwnerID]
		if !exists {
			return nil, xerrors.Errorf("owner not found for workspace: %q", workspace.Name)
		}
		initiator, exists := userByID[build.InitiatorID]
		if !exists {
			return nil, xerrors.Errorf("build initiator not found for workspace: %q", workspace.Name)
		}

		resources := resourcesByJobID[job.ID]
		var apiResources []codersdk.WorkspaceResource
		for _, resource := range resources {
			apiAgents := make([]codersdk.WorkspaceAgent, 0)
			agents := agentsByResourceID[resource.ID]
			for _, agent := range agents {
				apps := appsByAgentID[agent.ID]
				apiAgent, err := convertWorkspaceAgent(api.DERPMap, api.TailnetCoordinator, agent, convertApps(apps), api.AgentInactiveDisconnectTimeout)
				if err != nil {
					return nil, xerrors.Errorf("converting workspace agent: %w", err)
				}
				apiAgents = append(apiAgents, apiAgent)
			}
			metadata := metadataByResourceID[resource.ID]
			apiResources = append(apiResources, convertWorkspaceResource(resource, apiAgents, metadata))
		}

		apiBuilds = append(apiBuilds, codersdk.WorkspaceBuild{
			ID:                 build.ID,
			CreatedAt:          build.CreatedAt,
			UpdatedAt:          build.UpdatedAt,
			WorkspaceOwnerID:   workspace.OwnerID,
			WorkspaceOwnerName: owner.Username,
			WorkspaceID:        build.WorkspaceID,
			WorkspaceName:      workspace.Name,
			TemplateVersionID:  build.TemplateVersionID,
			BuildNumber:        build.BuildNumber,
			Transition:         codersdk.WorkspaceTransition(build.Transition),
			InitiatorID:        build.InitiatorID,
			InitiatorUsername:  initiator.Username,
			Job:                convertProvisionerJob(job),
			Deadline:           codersdk.NewNullTime(build.Deadline, !build.Deadline.IsZero()),
			Reason:             codersdk.BuildReason(build.Reason),
			Resources:          apiResources,
		})
	}

	return apiBuilds, nil
}

func convertWorkspaceResource(resource database.WorkspaceResource, agents []codersdk.WorkspaceAgent, metadata []database.WorkspaceResourceMetadatum) codersdk.WorkspaceResource {
	metadataMap := map[string]database.WorkspaceResourceMetadatum{}

	// implicit metadata fields come first
	metadataMap["type"] = database.WorkspaceResourceMetadatum{
		Key:       "type",
		Value:     sql.NullString{String: resource.Type, Valid: true},
		Sensitive: false,
	}
	// explicit metadata fields come afterward, and can override implicit ones
	for _, field := range metadata {
		metadataMap[field.Key] = field
	}

	var convertedMetadata []codersdk.WorkspaceResourceMetadata
	for _, field := range metadataMap {
		if field.Value.Valid {
			convertedField := codersdk.WorkspaceResourceMetadata{
				Key:       field.Key,
				Value:     field.Value.String,
				Sensitive: field.Sensitive,
			}
			convertedMetadata = append(convertedMetadata, convertedField)
		}
	}
	slices.SortFunc(convertedMetadata, func(a, b codersdk.WorkspaceResourceMetadata) bool {
		return a.Key < b.Key
	})

	return codersdk.WorkspaceResource{
		ID:         resource.ID,
		CreatedAt:  resource.CreatedAt,
		JobID:      resource.JobID,
		Transition: codersdk.WorkspaceTransition(resource.Transition),
		Type:       resource.Type,
		Name:       resource.Name,
		Hide:       resource.Hide,
		Icon:       resource.Icon,
		Agents:     agents,
		Metadata:   convertedMetadata,
	}
}
