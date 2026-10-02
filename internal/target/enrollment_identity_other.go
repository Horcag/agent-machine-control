//go:build !unix && !windows

package target

import "os"

func enrollmentFileIdentity(*os.File) (string, error) { return "", ErrHostSecurityUnproven }
