package selfupdate

// PublicRepository is the repository airlock is released from. Builds of the former private
// repository read their updates from it too, so airlock update moved their users over.
const PublicRepository = "n2jsoft-public-org/airlock"

// Repository returns the owner/repo airlock is released from.
func Repository() (string, error) {
	return PublicRepository, nil
}
