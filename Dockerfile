FROM golang:1.26-alpine AS build
RUN apk add --no-cache git gcc musl-dev
RUN git config --global --add safe.directory '*'

COPY . /code
WORKDIR /code
RUN CGO_ENABLED=1 go generate ./...
RUN CGO_ENABLED=1 go build ./cmd/cog

FROM alpine
RUN apk add --no-cache \
	tesseract-ocr \
	tesseract-ocr-data-eng \
	ffmpeg \
	ca-certificates
# LibreOffice is on its own line to minimize the size of the layer
RUN apk add --no-cache --no-scripts \
	libreoffice-writer \
	libreoffice-calc \
	libreoffice-impress \
	font-noto
COPY --from=build /code/cog /usr/bin/cog
RUN cog completion bash > /etc/bash/bash_completion.d/cog 2>/dev/null || true
EXPOSE 8580
ENV COG_ADDR=:8580 COG_DB=/data/system.db COG_DATA_DIR=/data
CMD [ "/usr/bin/cog" ]
