package provider

func Find(registrations []*A2ARegistration, appName string) *A2ARegistration {
	for _, registration := range registrations {
		if registration.AppName == appName {
			return registration
		}
	}

	return nil
}
