// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package progress

import (
	"fmt"
	"io"
	"sync"

	"github.com/aws/copilot-cli/internal/pkg/aws/cloudformation"
	"github.com/aws/copilot-cli/internal/pkg/stream"
)

// StackSubscriber is the interface to subscribe channels to a CloudFormation stack stream event.
type StackSubscriber interface {
	Subscribe(channels ...chan stream.StackEvent)
}

// StackResourceDescription identifies a CloudFormation stack resource annotated with a human-friendly description.
type StackResourceDescription struct {
	LogicalResourceID string
	ResourceType      string
	Description       string
}

// ListeningStackRenderer returns a tab-separated component that listens for CloudFormation
// resource events from a stack until the streamer stops.
//
// The component only listens for stack resource events for the provided changes in the stack.
// The state of changes is updated as events are published from the streamer.
func ListeningStackRenderer(streamer StackSubscriber, stackName, description string, changes []StackResourceDescription) Renderer {
	var children []Renderer
	for _, change := range changes {
		children = append(children, listeningResourceRenderer(streamer, change, nestedComponentPadding))
	}
	comp := &stackComponent{
		logicalID:   stackName,
		description: description,
		statuses:    []stackStatus{notStartedStackStatus},
		children:    children,
		stream:      make(chan stream.StackEvent),
		separator:   '\t',
	}
	streamer.Subscribe(comp.stream)
	go comp.Listen()
	return comp
}

// listeningResourceRenderer returns a tab-separated component that listens for
// CloudFormation stack events for a particular resource.
func listeningResourceRenderer(streamer StackSubscriber, resource StackResourceDescription, padding int) Renderer {
	comp := &regularResourceComponent{
		logicalID:   resource.LogicalResourceID,
		description: resource.Description,
		statuses:    []stackStatus{notStartedStackStatus},
		stream:      make(chan stream.StackEvent),
		padding:     padding,
		separator:   '\t',
	}
	streamer.Subscribe(comp.stream)
	go comp.Listen()
	return comp
}

// stackComponent can display a CloudFormation stack and all of its associated resources.
type stackComponent struct {
	logicalID   string        // The CloudFormation stack name.
	description string        // The human friendly explanation of the purpose of the stack.
	statuses    []stackStatus // In-order history of the CloudFormation status of the stack throughout the deployment.
	children    []Renderer    // Resources part of the stack.

	padding   int  // Leading spaces before rendering the stack.
	separator rune // Character used to separate columns of text.

	stream chan stream.StackEvent
	mu     sync.Mutex
}

// Listen updates the stack's status if a CloudFormation stack event is received.
func (c *stackComponent) Listen() {
	for ev := range c.stream {
		updateComponentStatus(&c.mu, &c.statuses, c.logicalID, ev)
	}
}

// Render prints the CloudFormation stack's resource components in order and returns the number of lines written.
// If any sub-component's Render call fails, then writes nothing and returns an error.
func (c *stackComponent) Render(out io.Writer) (numLines int, err error) {
	c.mu.Lock()
	components := stackResourceComponents(c.description, c.separator, c.statuses, c.padding)
	c.mu.Unlock()

	return renderComponents(out, append(components, c.children...))
}

// regularResourceComponent can display a simple CloudFormation stack resource event.
type regularResourceComponent struct {
	logicalID   string        // The LogicalID defined in the template for the resource.
	description string        // The human friendly explanation of the resource.
	statuses    []stackStatus // In-order history of the CloudFormation status of the resource throughout the deployment.

	padding   int  // Leading spaces before rendering the resource.
	separator rune // Character used to separate columns of text.

	stream chan stream.StackEvent
	mu     sync.Mutex
}

// Listen updates the resource's status if a CloudFormation stack resource event is received.
func (c *regularResourceComponent) Listen() {
	for ev := range c.stream {
		updateComponentStatus(&c.mu, &c.statuses, c.logicalID, ev)
	}
}

// Render prints the resource as a singleLineComponent and returns the number of lines written and the error if any.
func (c *regularResourceComponent) Render(out io.Writer) (numLines int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	components := stackResourceComponents(c.description, c.separator, c.statuses, c.padding)
	return renderComponents(out, components)
}

func updateComponentStatus(mu *sync.Mutex, statuses *[]stackStatus, logicalID string, event stream.StackEvent) {
	if logicalID != event.LogicalResourceID {
		return
	}
	mu.Lock()
	*statuses = append(*statuses, stackStatus{
		value:  cloudformation.StackStatus(event.ResourceStatus),
		reason: event.ResourceStatusReason,
	})
	mu.Unlock()
}

func stackResourceComponents(description string, separator rune, statuses []stackStatus, padding int) []Renderer {
	components := []Renderer{
		&singleLineComponent{
			Text:    fmt.Sprintf("- %s%s%s", description, string(separator), prettifyLatestStackStatus(statuses)),
			Padding: padding,
		},
	}

	for _, failureReason := range failureReasons(statuses) {
		for _, text := range splitByLength(failureReason, maxCellLength) {
			components = append(components, &singleLineComponent{
				Text:    fmt.Sprintf("%s%s", colorFailureReason(text), string(separator)),
				Padding: padding + nestedComponentPadding,
			})
		}
	}
	return components
}

func renderComponents(out io.Writer, components []Renderer) (numLines int, err error) {
	for _, comp := range components {
		nl, err := comp.Render(out)
		if err != nil {
			return 0, err
		}
		numLines += nl
	}
	return numLines, nil
}