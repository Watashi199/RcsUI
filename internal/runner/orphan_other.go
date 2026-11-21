//go:build !linux

package runner

// killOrphan : sans /proc, impossible de vérifier qu'un pid appartient bien
// au run ; on ne tue rien (plateformes de développement uniquement).
func killOrphan(pid int, runID string) (bool, error) { return false, nil }
