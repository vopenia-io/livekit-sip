package utils

import (
	"errors"
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

func ElementSetPropertyMany(element *gst.Element, properties map[string]interface{}) error {
	if element == nil {
		return errors.New("element is nil")
	}
	if len(properties) == 0 {
		return nil
	}
	var errs []error
	for key, value := range properties {
		if err := element.SetProperty(key, value); err != nil {
			errs = append(errs, fmt.Errorf("failed to set property %q: %w", key, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to set properties on %q: %w", element.GetName(), errors.Join(errs...))
	}
	return nil
}
