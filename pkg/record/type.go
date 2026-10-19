package record

type Type byte

const (
	TypeNull      Type = 0x00 // Null value
	TypeInt64     Type = 0x01 // 64-bit signed integer
	TypeBool      Type = 0x02 // Boolean value
	TypeText      Type = 0x03 // UTF-8 encoded text
	TypeBytes     Type = 0x04 // Arbitrary binary data
	TypeFloat64   Type = 0x05 // 64-bit floating-point number
	TypeJSON      Type = 0x06 // JSON-encoded data
	TypeDate      Type = 0x07 // Date value (YYYY-MM-DD)
	TypeTime      Type = 0x08 // Time value (HH:MM:SS)
	TypeDateTime  Type = 0x09 // Date and time value (YYYY-MM-DD HH:MM:SS)
	TypeUUID      Type = 0x0A // Universally Unique Identifier
	TypeDecimal   Type = 0x0B // Exact decimal value with precision and scale
	TypeMap       Type = 0x0C // Map of key-value pairs
	TypeTimestamp Type = 0x0D // Timestamp value (seconds since epoch)
	TypeUint64    Type = 0x0E // 64-bit unsigned integer
	TypeFloat32   Type = 0x0F // 32-bit floating-point number
	TypeDuration  Type = 0x10 // Duration value (in seconds)
	TypeArray     Type = 0x11 // Array of values
)
