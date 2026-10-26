package selfupdate

// PublicRepository is the repository airlock is released from. Builds of the former private
// repository read their updates from it too, so airlock update moves their users over.
const PublicRepository = "n2jsoft-public-org/airlock"

// PublicURL is the web address of PublicRepository.
const PublicURL = "https://github.com/" + PublicRepository

// Repository returns the owner/repo airlock is released from.
func Repository() (string, error) {
	return PublicRepository, nil
}
