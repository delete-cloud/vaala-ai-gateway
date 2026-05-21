package consts

// Channel provider types (numeric IDs used in database).
// Values match new-api's constant.ChannelType* constants.
const (
	ChannelTypeOpenAI    = 1
	ChannelTypeAzure     = 3
	ChannelTypeOllama    = 4
	ChannelTypeAnthropic = 14
	ChannelTypeGemini    = 24
	ChannelTypeAWS       = 33
	ChannelTypeVertexAI  = 41

	// ChannelTypeGitHubCopilot is a Vaala-native channel type. It does not
	// come from new-api's channel table.
	ChannelTypeGitHubCopilot = 1001
)
