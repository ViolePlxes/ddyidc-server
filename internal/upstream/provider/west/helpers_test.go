package west

import "strconv"

// options builds the ID-keyed configuration map the platform sends: the order
// snapshot stores the chosen value under the config option ID.
func options(pairs map[int]string) map[string]string {
	out := make(map[string]string, len(pairs))
	for id, value := range pairs {
		out[strconv.Itoa(id)] = value
	}
	return out
}
