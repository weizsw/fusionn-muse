package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

const maxSubmissionBodyBytes = 1 << 20

func writeSubmissionInputError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		status = http.StatusRequestEntityTooLarge
	}
	c.JSON(status, gin.H{"error": err.Error()})
}
