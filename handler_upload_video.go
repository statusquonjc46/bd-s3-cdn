package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func processVideoForFastStart(filePath string) (string, error) {
	outFile := filePath + ".Processing"
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outFile)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("Failed to get aspect ratio from ffprobe: %v | %v", err, stderr.String())
	}
	return outFile, err
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("Failed to get aspect ratio from ffprobe: %v | %v", err, stderr.String())
	}

	type probeData struct {
		Streams []struct {
			Index              int    `json:"index"`
			CodecName          string `json:"codec_name,omitempty"`
			CodecLongName      string `json:"codec_long_name,omitempty"`
			Profile            string `json:"profile,omitempty"`
			CodecType          string `json:"codec_type"`
			CodecTagString     string `json:"codec_tag_string"`
			CodecTag           string `json:"codec_tag"`
			Width              int    `json:"width,omitempty"`
			Height             int    `json:"height,omitempty"`
			CodedWidth         int    `json:"coded_width,omitempty"`
			CodedHeight        int    `json:"coded_height,omitempty"`
			ClosedCaptions     int    `json:"closed_captions,omitempty"`
			FilmGrain          int    `json:"film_grain,omitempty"`
			HasBFrames         int    `json:"has_b_frames,omitempty"`
			SampleAspectRatio  string `json:"sample_aspect_ratio,omitempty"`
			DisplayAspectRatio string `json:"display_aspect_ratio,omitempty"`
			PixFmt             string `json:"pix_fmt,omitempty"`
			Level              int    `json:"level,omitempty"`
			ColorRange         string `json:"color_range,omitempty"`
			ColorSpace         string `json:"color_space,omitempty"`
			ColorTransfer      string `json:"color_transfer,omitempty"`
			ColorPrimaries     string `json:"color_primaries,omitempty"`
			ChromaLocation     string `json:"chroma_location,omitempty"`
			FieldOrder         string `json:"field_order,omitempty"`
			Refs               int    `json:"refs,omitempty"`
			IsAvc              string `json:"is_avc,omitempty"`
			NalLengthSize      string `json:"nal_length_size,omitempty"`
			ID                 string `json:"id"`
			RFrameRate         string `json:"r_frame_rate"`
			AvgFrameRate       string `json:"avg_frame_rate"`
			TimeBase           string `json:"time_base"`
			StartPts           int    `json:"start_pts"`
			StartTime          string `json:"start_time"`
			DurationTs         int    `json:"duration_ts"`
			Duration           string `json:"duration"`
			BitRate            string `json:"bit_rate,omitempty"`
			BitsPerRawSample   string `json:"bits_per_raw_sample,omitempty"`
			NbFrames           string `json:"nb_frames"`
			ExtradataSize      int    `json:"extradata_size"`
			Disposition        struct {
				Default         int `json:"default"`
				Dub             int `json:"dub"`
				Original        int `json:"original"`
				Comment         int `json:"comment"`
				Lyrics          int `json:"lyrics"`
				Karaoke         int `json:"karaoke"`
				Forced          int `json:"forced"`
				HearingImpaired int `json:"hearing_impaired"`
				VisualImpaired  int `json:"visual_impaired"`
				CleanEffects    int `json:"clean_effects"`
				AttachedPic     int `json:"attached_pic"`
				TimedThumbnails int `json:"timed_thumbnails"`
				NonDiegetic     int `json:"non_diegetic"`
				Captions        int `json:"captions"`
				Descriptions    int `json:"descriptions"`
				Metadata        int `json:"metadata"`
				Dependent       int `json:"dependent"`
				StillImage      int `json:"still_image"`
			} `json:"disposition"`
			Tags struct {
				Language    string `json:"language"`
				HandlerName string `json:"handler_name"`
				VendorID    string `json:"vendor_id"`
				Encoder     string `json:"encoder"`
				Timecode    string `json:"timecode"`
			} `json:"tags"`
			SampleFmt      string `json:"sample_fmt,omitempty"`
			SampleRate     string `json:"sample_rate,omitempty"`
			Channels       int    `json:"channels,omitempty"`
			ChannelLayout  string `json:"channel_layout,omitempty"`
			BitsPerSample  int    `json:"bits_per_sample,omitempty"`
			InitialPadding int    `json:"initial_padding,omitempty"`
		} `json:"streams"`
	}
	probe := probeData{}
	err = json.Unmarshal(out.Bytes(), &probe)
	if err != nil {
		return "", fmt.Errorf("Failed to unmarshal ffprobe output: %v", err)
	}

	width := probe.Streams[0].Width
	height := probe.Streams[0].Height
	landscapeDiff := width*9 - height*16
	portraitDiff := width*16 - height*9

	if landscapeDiff < 0 {
		landscapeDiff = -landscapeDiff
	}
	if portraitDiff < 0 {
		portraitDiff = -portraitDiff
	}

	if landscapeDiff < 60 {
		return "16:9", nil
	} else if portraitDiff < 60 {
		return "9:16", nil
	} else {
		return "other", nil
	}
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	// 1GB upload limit
	const uploadLimit = 1 << 30
	// overwrites r.body with max 1GB file limit
	r.Body = http.MaxBytesReader(w, r.Body, uploadLimit)

	// get id from endpoit call and parse to UUID for DB lookup
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	// check bearer token for authentication
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	// validate bearer with JWT
	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// get video data by videa ID from DB.
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

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "failed to get the thumbnail", err)
		return
	}
	defer file.Close()

	// get the content type of the request
	mediaType := header.Header.Get("Content-Type")
	mimeType, _, err := mime.ParseMediaType(mediaType)

	if mimeType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Uploaded File not the correct type", err)
		return
	}

	filePointer, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to create file path", err)
		return
	}
	defer os.Remove(filePointer.Name())
	defer filePointer.Close()

	_, err = io.Copy(filePointer, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to copy file to /assets", err)
		return
	}
	defer filePointer.Close()

	_, err = io.Copy(filePointer, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to copy file to /assets", err)
		return
	}
	defer filePointer.Close()

	_, err = filePointer.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to set file to start", err)
		return
	}

	randBytes := make([]byte, 32)
	rand.Read(randBytes)
	randId := base64.RawURLEncoding.EncodeToString(randBytes)
	fileExt := strings.Split(mediaType, "/")[1]
	fileName := fmt.Sprintf("%v.%s", randId, fileExt)
	aspectRatio, err := getVideoAspectRatio(filePointer.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to get aspect ratio", err)
		return
	}
	s3key := ""
	switch aspectRatio {
	case "16:9":
		s3key = "landscape/" + fileName
	case "9:16":
		s3key = "portrait/" + fileName
	default:
		s3key = "other/" + fileName
	}

	processed, err := processVideoForFastStart(filePointer.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to process video", err)
		return
	}
	fp, err := os.Open(processed)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to open processing file", err)
		return
	}
	defer os.Remove(fp.Name())
	defer fp.Close()

	_, err = cfg.s3client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &s3key,
		Body:        fp,
		ContentType: &mimeType,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to upload to s3", err)
		return
	}

	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, s3key)
	videoData.VideoURL = &videoURL
	err = cfg.db.UpdateVideo(videoData)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update video in DB", err)
		return
	}

	respondWithJSON(w, http.StatusOK, videoData)
}
