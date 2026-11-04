package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// bitwise operator to create 10MB
	const maxMemory = 10 << 20
	// Parse into multipart upload
	r.ParseMultipartForm(maxMemory)

	// get the file data, header, error from the thumbnile file
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "failed to get the thumbnail", err)
		return
	}

	// get the content type of the request
	mediaType := header.Header.Get("Content-Type")
	imageData, err := io.ReadAll(file)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "failed to get content-type from headers", err)
	}

	// get the video data in a struct
	videoData, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to find video in database", err)
		return
	}

	// confirm jwt matches upload user id
	if videoData.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "video user id does not match user performing action", nil)
		return
	}

	// struct to store into the thumbnails map
	thumbnailData := thumbnail{
		data:      imageData,
		mediaType: mediaType,
	}
	b64 := base64.StdEncoding.EncodeToString(thumbnailData.data)
	dataURL := fmt.Sprintf("data:%v;base64,%v", thumbnailData.mediaType, b64)
	videoData.ThumbnailURL = &dataURL

	err = cfg.db.UpdateVideo(videoData)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to add video to database", err)
		return
	}

	respondWithJSON(w, http.StatusOK, videoData)
}
