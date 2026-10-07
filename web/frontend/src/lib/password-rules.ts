/** The shortest dashboard password the server accepts. */
export const MIN_PASSWORD_LENGTH = 8

export interface SetupPasswordErrors {
  password?: "required" | "tooShort"
  confirm?: "required" | "mismatch"
}

/**
 * Checks a new password and its confirmation the way the server does: both
 * are trimmed, and the length counts characters rather than UTF-16 units.
 */
export function validateSetupPassword(
  password: string,
  confirm: string,
): SetupPasswordErrors {
  const errors: SetupPasswordErrors = {}
  const pw = password.trim()
  const repeated = confirm.trim()
  if (!pw) errors.password = "required"
  else if (Array.from(pw).length < MIN_PASSWORD_LENGTH)
    errors.password = "tooShort"
  if (!repeated) errors.confirm = "required"
  else if (pw && repeated !== pw) errors.confirm = "mismatch"
  return errors
}
