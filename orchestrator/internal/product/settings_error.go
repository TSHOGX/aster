package product

// SettingError exposes only an administrator-authored field and a safe reason.
// It retains the established machine code and invalid-setting classification.
type SettingError struct{ Field, Reason string }

func (e *SettingError) Error() string { return e.Field + ": " + e.Reason }
func (e *SettingError) Unwrap() error { return Admission("environment_invalid:"+e.Field, ErrInvalid) }
