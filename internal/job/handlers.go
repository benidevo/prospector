package job

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/benidevo/ascentio/internal/config"
	"github.com/benidevo/ascentio/internal/job/models"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// JobHandler manages job-related HTTP requests
type JobHandler struct {
	service        *JobService
	cfg            *config.Settings
	commandFactory *CommandFactory
}

// formatValidationError converts validator errors to user-friendly messages
func (h *JobHandler) formatValidationError(err error) string {
	if validationErrs, ok := err.(validator.ValidationErrors); ok {
		for _, e := range validationErrs {
			// Check field and tag combinations
			field := e.Field()
			tag := e.Tag()

			switch field {
			case "Title":
				switch tag {
				case "required":
					return models.ErrJobTitleRequired.Error()
				case "min":
					return "Job title cannot be empty"
				case "max":
					return "Job title must be less than 255 characters"
				}
			case "Description":
				switch tag {
				case "required":
					return models.ErrJobDescriptionRequired.Error()
				case "min":
					return "Job description cannot be empty"
				}
			case "Company.Name", "Name": // Handle both nested and flat field names
				switch tag {
				case "required":
					return models.ErrCompanyNameRequired.Error()
				case "min":
					return "Company name cannot be empty"
				case "max":
					return "Company name must be less than 255 characters"
				}
			case "Location":
				if tag == "max" {
					return "Location must be less than 255 characters"
				}
			case "Notes":
				if tag == "max" {
					return "Notes must be less than 5000 characters"
				}
			case "RequiredSkills":
				switch tag {
				case "max":
					return "Cannot have more than 50 skills"
				case "dive": // This happens when validating array elements
					return "Invalid skill entry"
				}
			case "SourceURL", "ApplicationURL":
				switch tag {
				case "url":
					return models.ErrInvalidURLFormat.Error()
				case "omitempty": // Skip this tag
					continue
				}
			case "Status":
				switch tag {
				case "min", "max":
					return models.ErrInvalidJobStatus.Error()
				}
			case "JobType":
				switch tag {
				case "min", "max":
					return "Invalid job type"
				}
			case "ExperienceLevel":
				switch tag {
				case "min", "max":
					return "Invalid experience level"
				}
			}

			// If we got here, no specific message was found but we have an error
			// Return a generic message based on the tag
			switch tag {
			case "required":
				return fmt.Sprintf("%s is required", field)
			case "min":
				return fmt.Sprintf("%s is too short", field)
			case "max":
				return fmt.Sprintf("%s is too long", field)
			case "url":
				return fmt.Sprintf("%s must be a valid URL", field)
			default:
				return fmt.Sprintf("Invalid %s", strings.ToLower(field))
			}
		}
	}
	return err.Error()
}

// renderError is a helper function to render error messages with appropriate status codes
func (h *JobHandler) renderError(c *gin.Context, err error) {
	sentinelErr := models.GetSentinelError(err)
	statusCode := http.StatusInternalServerError

	// Check if it's a validation error
	if _, ok := err.(validator.ValidationErrors); ok {
		statusCode = http.StatusBadRequest
		errorMessage := h.formatValidationError(err)
		c.HTML(statusCode, "partials/alert-error.html", gin.H{
			"message": errorMessage,
		})
		return
	}

	// Determine appropriate status code based on error type
	if errors.Is(err, models.ErrInvalidJobIDFormat) ||
		errors.Is(err, models.ErrInvalidJobID) ||
		errors.Is(err, models.ErrInvalidFieldParam) ||
		errors.Is(err, models.ErrFieldRequired) ||
		errors.Is(err, models.ErrInvalidJobStatus) ||
		errors.Is(err, models.ErrStatusRequired) ||
		errors.Is(err, models.ErrJobTitleRequired) ||
		errors.Is(err, models.ErrCompanyNameRequired) ||
		errors.Is(err, models.ErrInvalidStatusTransition) ||
		errors.Is(err, models.ErrJobDescriptionRequired) ||
		errors.Is(err, models.ErrCompanyRequired) ||
		errors.Is(err, models.ErrInvalidURLFormat) {
		statusCode = http.StatusBadRequest
	} else if errors.Is(err, models.ErrJobNotFound) {
		statusCode = http.StatusNotFound
	}

	c.HTML(statusCode, "partials/alert-error.html", gin.H{
		"message": sentinelErr.Error(),
	})
}

// renderDashboardError is a helper function specifically for dashboard error messages
func (h *JobHandler) renderDashboardError(c *gin.Context, err error) {
	sentinelErr := models.GetSentinelError(err)
	statusCode := http.StatusInternalServerError

	// Check if it's a validation error
	if _, ok := err.(validator.ValidationErrors); ok {
		statusCode = http.StatusBadRequest
		errorMessage := h.formatValidationError(err)
		c.HTML(statusCode, "partials/alert-error-dashboard.html", gin.H{
			"message": errorMessage,
		})
		return
	}

	// Determine appropriate status code based on error type
	if errors.Is(err, models.ErrInvalidJobStatus) ||
		errors.Is(err, models.ErrStatusRequired) ||
		errors.Is(err, models.ErrInvalidStatusTransition) {
		statusCode = http.StatusBadRequest
	}

	c.HTML(statusCode, "partials/alert-error-dashboard.html", gin.H{
		"message": sentinelErr.Error(),
	})
}

// NewJobHandler creates and returns a new JobHandler with the provided JobService and configuration settings.
func NewJobHandler(service *JobService, cfg *config.Settings) *JobHandler {
	return &JobHandler{
		service:        service,
		cfg:            cfg,
		commandFactory: NewCommandFactory(),
	}
}

// ValidateJobID is a middleware that validates the job ID parameter
func (h *JobHandler) ValidateJobID() gin.HandlerFunc {
	return func(c *gin.Context) {
		jobIDStr := c.Param("id")
		if jobIDStr == "" {
			c.Next()
			return
		}

		jobID, err := h.service.ValidateJobIDFormat(jobIDStr)
		if err != nil {
			h.renderError(c, models.ErrInvalidJobIDFormat)
			c.Abort()
			return
		}

		// Store the validated job ID in the context
		c.Set("jobID", jobID)
		c.Next()
	}
}

// ListJobsPage handles the HTTP request to display the jobs dashboard page.
// It retrieves the current user's jobs, applies optional status filtering,
// gathers job statistics, and renders the dashboard template with the results.
func (h *JobHandler) ListJobsPage(c *gin.Context) {
	username, _ := c.Get("username")
	statusParam := c.Query("status")

	filter := models.JobFilter{
		Limit: 50,
	}

	if statusParam != "" && statusParam != "all" {
		jobStatus, err := models.JobStatusFromString(statusParam)
		if err == nil {
			filter.Status = &jobStatus
		}
	}

	jobs, err := h.service.GetJobs(c.Request.Context(), filter)
	if err != nil {
		c.HTML(http.StatusInternalServerError, "layouts/base.html", gin.H{
			"title":        "Dashboard",
			"page":         "dashboard",
			"activeNav":    "jobs",
			"pageTitle":    "Job Matches",
			"currentYear":  time.Now().Year(),
			"username":     username,
			"jobs":         []*models.Job{},
			"statusFilter": statusParam,
		})
		return
	}

	stats := h.service.GetJobStats(c.Request.Context())

	c.HTML(http.StatusOK, "layouts/base.html", gin.H{
		"title":        "Dashboard",
		"page":         "dashboard",
		"activeNav":    "jobs",
		"pageTitle":    "Job Matches",
		"currentYear":  time.Now().Year(),
		"username":     username,
		"jobs":         jobs,
		"totalJobs":    stats.TotalJobs,
		"applied":      stats.TotalApplied,
		"highMatch":    1, // Keeping this dummy data for now
		"statusFilter": statusParam,
	})
}

// GetNewJobForm renders the form for adding a new job.
// It populates the template with user and page information.
func (h *JobHandler) GetNewJobForm(c *gin.Context) {
	username, _ := c.Get("username")
	c.HTML(http.StatusOK, "layouts/base.html", gin.H{
		"title":       "Add New Job",
		"page":        "job-new",
		"activeNav":   "newjob",
		"pageTitle":   "Add New Job",
		"currentYear": time.Now().Year(),
		"username":    username,
	})
}

// CreateJob handles form submission for creating a new job
func (h *JobHandler) CreateJob(c *gin.Context) {
	title := strings.TrimSpace(c.PostForm("title"))
	description := strings.TrimSpace(c.PostForm("description"))
	companyName := strings.TrimSpace(c.PostForm("company_name"))
	location := strings.TrimSpace(c.PostForm("location"))
	sourceURL := strings.TrimSpace(c.PostForm("source_url"))
	applicationURL := strings.TrimSpace(c.PostForm("application_url"))
	notes := strings.TrimSpace(c.PostForm("notes"))

	if err := h.service.ValidateURL(sourceURL); err != nil {
		h.renderError(c, err)
		return
	}

	if err := h.service.ValidateURL(applicationURL); err != nil {
		h.renderError(c, err)
		return
	}

	skillsStr := c.PostForm("skills")
	skills := h.service.ValidateAndFilterSkills(skillsStr)

	jobTypeStr := c.PostForm("job_type")
	jobType := models.JobTypeFromString(jobTypeStr)

	expLevelStr := c.PostForm("experience_level")
	expLevel := models.ExperienceLevelFromString(expLevelStr)

	statusStr := c.PostForm("status")
	status, err := models.JobStatusFromString(statusStr)
	if err != nil {
		h.renderError(c, err)
		return
	}

	options := []models.JobOption{
		models.WithJobType(jobType),
		models.WithExperienceLevel(expLevel),
		models.WithStatus(status),
		models.WithRequiredSkills(skills),
	}

	if location != "" {
		options = append(options, models.WithLocation(location))
	}
	if sourceURL != "" {
		options = append(options, models.WithSourceURL(sourceURL))
	}
	if applicationURL != "" {
		options = append(options, models.WithApplicationURL(applicationURL))
	}
	if notes != "" {
		options = append(options, models.WithNotes(notes))
	}

	job, err := h.service.CreateJob(c.Request.Context(), title, description, companyName, options...)
	if err != nil {
		h.renderError(c, err)
		return
	}

	c.HTML(http.StatusOK, "partials/alert-success.html", gin.H{
		"message": "Job created successfully!",
		"jobID":   strconv.Itoa(job.ID),
	})
}

// GetJobDetails handles the HTTP request to retrieve and display details for a specific job.
// It validates the job ID, fetches job data from the service layer, and renders the appropriate HTML template.
// Returns a 400 error for invalid IDs, 404 if the job is not found, or 500 for other errors.
func (h *JobHandler) GetJobDetails(c *gin.Context) {
	if h.cfg != nil && h.cfg.IsTest {
		c.Status(http.StatusOK)
		return
	}

	jobIDValue, exists := c.Get("jobID")
	if !exists {
		h.renderError(c, models.ErrInvalidJobIDFormat)
		return
	}
	jobID := jobIDValue.(int)
	jobIDStr := c.Param("id")

	job, err := h.service.GetJob(c.Request.Context(), jobID)
	if err != nil {
		if errors.Is(err, models.ErrJobNotFound) {
			c.HTML(http.StatusNotFound, "layouts/base.html", gin.H{
				"title":       "Page Not Found",
				"page":        "404",
				"currentYear": time.Now().Year(),
			})
			return
		}
		h.renderError(c, err)
		return
	}

	username, _ := c.Get("username")
	c.HTML(http.StatusOK, "layouts/base.html", gin.H{
		"title":       "Job Details",
		"page":        "job-details",
		"activeNav":   "jobs",
		"pageTitle":   "Job Details",
		"currentYear": time.Now().Year(),
		"username":    username,
		"job":         job,
		"jobID":       jobIDStr,
	})
}

// UpdateJobField handles the request to update a specific job field
func (h *JobHandler) UpdateJobField(c *gin.Context) {
	jobIDValue, exists := c.Get("jobID")
	if !exists {
		h.renderError(c, models.ErrInvalidJobIDFormat)
		return
	}
	jobID := jobIDValue.(int)

	field := c.Param("field")
	err := h.service.ValidateFieldName(field)
	if err != nil {
		h.renderError(c, err)
		return
	}

	job, err := h.service.GetJob(c.Request.Context(), jobID)
	if err != nil {
		h.renderError(c, err)
		return
	}

	// Get the appropriate command for the field
	command, err := h.commandFactory.GetCommand(field)
	if err != nil {
		h.renderError(c, err)
		return
	}

	// Execute the command
	successMessage, err := command.Execute(c, job, h.service)
	if err != nil {
		// Use dashboard-specific error format for status updates
		if field == "status" {
			h.renderDashboardError(c, err)
		} else {
			h.renderError(c, err)
		}
		return
	}

	err = h.service.UpdateJob(c.Request.Context(), job)
	if err != nil {
		// Use dashboard-specific error format for status updates
		if field == "status" {
			h.renderDashboardError(c, err)
		} else {
			h.renderError(c, err)
		}
		return
	}

	// Use dashboard-specific alert for all status updates
	if field == "status" {
		c.HTML(http.StatusOK, "partials/alert-success-dashboard.html", gin.H{
			"message": successMessage,
		})
		return
	}

	c.HTML(http.StatusOK, "partials/alert-success-detail.html", gin.H{
		"message": successMessage,
	})
}

// DeleteJob handles the request to delete a job
func (h *JobHandler) DeleteJob(c *gin.Context) {
	jobIDValue, exists := c.Get("jobID")
	if !exists {
		h.renderError(c, models.ErrInvalidJobIDFormat)
		return
	}
	jobID := jobIDValue.(int)

	err := h.service.DeleteJob(c.Request.Context(), jobID)
	if err != nil {
		h.renderError(c, err)
		return
	}

	if c.GetHeader("HX-Request") == "true" {
		// This will immediately redirect the browser without showing any intermediate content
		c.Header("HX-Redirect", "/jobs")
		c.String(http.StatusOK, "")
		return
	}

	c.Redirect(http.StatusFound, "/jobs")
}
