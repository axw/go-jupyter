package jupyter

import (
	"crypto/rand"
	"fmt"
	"io"
)

// newUUID returns a new v4 (pseudo-random) UUID, in hex-encoded form.
func newUUID() (string, error) {
	uuid := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, uuid); err != nil {
		return "", fmt.Errorf("reading random data: %v", err)
	}

	/* From https://www.ietf.org/rfc/rfc4122.txt, section 4.4:

	   o  Set the two most significant bits (bits 6 and 7) of the
	      clock_seq_hi_and_reserved to zero and one, respectively.

	   o  Set the four most significant bits (bits 12 through 15) of the
	      time_hi_and_version field to the 4-bit version number from
	      Section 4.1.3.

	   o  Set all the other bits to randomly (or pseudo-randomly) chosen
	      values.
	*/

	const version = 4
	uuid[8] = (uuid[8] & 0x7F) | (1 << 6)
	uuid[6] = (uuid[6] & 0x0F) | (version << 4)
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		uuid[:4],
		uuid[4:6],
		uuid[6:8],
		uuid[8:10],
		uuid[10:],
	), nil
}
