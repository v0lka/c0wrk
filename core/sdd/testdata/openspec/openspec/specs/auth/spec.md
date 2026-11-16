# Auth Capability

## Purpose

Authenticate users of the fixture application.

## Requirements

### Requirement: Multi-factor authentication

The system SHALL offer a second factor to every account.

#### Scenario: Enrolled user signs in

- **WHEN** the user signs in with a password
- **THEN** a one-time code is requested

### Requirement: Session expiry

The system SHALL expire idle sessions.

#### Scenario: Idle session

- **WHEN** the session is idle for an hour
- **THEN** the user is signed out

## Notes

See the [billing capability](../billing/spec.md).
