package jupyter

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"strings"
)

const svgNamespace = "http://www.w3.org/2000/svg"

// renderDisplayData takes a value and renders it in one or more display-data
// representations.
func renderDisplayData(value interface{}) (data, metadata map[string]interface{}, err error) {
	// TODO(axw) sniff/provide a way to specify types for things that net/http does not sniff:
	// - SVG
	// - JSON
	// - LaTeX
	data = make(map[string]interface{})
	metadata = make(map[string]interface{})
	var stringValue, contentType string
	switch value := value.(type) {
	case image.Image:
		// TODO(axw) provide a way for the user to use alternative encodings?
		//
		// The current presumption is that images will be mostly used
		// for displaying graphs or illustrations where PNG produces
		// better looking images.
		var buf bytes.Buffer
		if err := png.Encode(&buf, value); err != nil {
			return nil, nil, fmt.Errorf("encoding image: %v", err)
		}
		bounds := value.Bounds()
		data["image/png"] = buf.Bytes()
		data["text/plain"] = fmt.Sprintf("%dx%d image", bounds.Dx(), bounds.Dy())
		metadata["image/png"] = map[string]interface{}{
			"width":  bounds.Dx(),
			"height": bounds.Dy(),
		}
		return data, metadata, nil

	case []byte:
		contentType = detectContentType(value)
		stringValue = string(value)

	case string:
		contentType = detectContentType([]byte(value))
		stringValue = value

	default:
		stringValue = fmt.Sprint(value)
		value = stringValue
		contentType = detectContentType([]byte(stringValue))
	}

	data[contentType] = value
	if contentType != "text/plain" {
		// "A plain text representation should always be
		// provided in the text/plain mime-type."
		data["text/plain"] = stringValue
	}
	return data, metadata, nil
}

func detectContentType(data []byte) string {
	contentType := http.DetectContentType(data)
	// Strip off the parameters ("; ...") from content-type;
	// Jupyter does not expect them.
	pos := strings.IndexRune(contentType, ';')
	if pos != -1 {
		contentType = contentType[:pos]
	}
	if contentType == "text/xml" {
		var document struct {
			XMLName xml.Name
		}
		// If we fail to unmarshal the XML, just return the
		// content-type as it is.
		if err := xml.Unmarshal(data, &document); err == nil {
			if document.XMLName.Space == svgNamespace {
				contentType = "image/svg+xml"
			}
		}
	}
	return contentType
}
