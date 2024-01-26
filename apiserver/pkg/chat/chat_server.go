/*
Copyright 2024 KubeAGI.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package chat

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tmc/langchaingo/memory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/kubeagi/arcadia/api/base/v1alpha1"
	"github.com/kubeagi/arcadia/apiserver/pkg/auth"
	"github.com/kubeagi/arcadia/apiserver/pkg/chat/storage"
	"github.com/kubeagi/arcadia/apiserver/pkg/client"
	"github.com/kubeagi/arcadia/pkg/appruntime"
	"github.com/kubeagi/arcadia/pkg/appruntime/base"
	"github.com/kubeagi/arcadia/pkg/appruntime/retriever"
	pkgconfig "github.com/kubeagi/arcadia/pkg/config"
	"github.com/kubeagi/arcadia/pkg/datasource"
)

type ChatServer struct {
	cli     dynamic.Interface
	storage storage.Storage
	once    sync.Once
}

func NewChatServer(cli dynamic.Interface) *ChatServer {
	return &ChatServer{
		cli: cli,
	}
}
func (cs *ChatServer) Storage() storage.Storage {
	if cs.storage == nil {
		cs.once.Do(func() {
			ctx := context.TODO()
			ds, err := pkgconfig.GetRelationalDatasource(ctx, nil, cs.cli)
			if err != nil || ds == nil {
				if err != nil {
					klog.Infof("get relational datasource failed: %s, use memory storage for chat", err.Error())
				} else if ds == nil {
					klog.Infoln("no relational datasource found, use memory storage for chat")
				}
				cs.storage = storage.NewMemoryStorage()
			}
			pg, err := datasource.GetPostgreSQLPool(ctx, nil, cs.cli, ds)
			if err != nil {
				klog.Errorf("get postgresql pool failed : %s", err.Error())
				cs.storage = storage.NewMemoryStorage()
				return
			}
			conn, err := pg.Pool.Acquire(ctx)
			if err != nil {
				klog.Errorf("postgresql pool acquire failed : %s", err.Error())
				cs.storage = storage.NewMemoryStorage()
				return
			}
			db, err := storage.NewPostgreSQLStorage(conn.Conn())
			if err != nil {
				klog.Errorf("storage.NewPostgreSQLStorage failed : %s", err.Error())
				cs.storage = storage.NewMemoryStorage()
				return
			}
			klog.Infoln("use pg as chat storage.")
			cs.storage = db
		})
	}
	return cs.storage
}

func (cs *ChatServer) AppRun(ctx context.Context, req ChatReqBody, respStream chan string, messageID string) (*ChatRespBody, error) {
	token := auth.ForOIDCToken(ctx)
	c, err := client.GetClient(token)
	if err != nil {
		return nil, fmt.Errorf("failed to get a dynamic client: %w", err)
	}
	obj, err := c.Resource(schema.GroupVersionResource{Group: v1alpha1.GroupVersion.Group, Version: v1alpha1.GroupVersion.Version, Resource: "applications"}).
		Namespace(req.AppNamespace).Get(ctx, req.APPName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get application: %w", err)
	}
	app := &v1alpha1.Application{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj.UnstructuredContent(), app)
	if err != nil {
		return nil, fmt.Errorf("failed to convert application: %w", err)
	}
	if !app.Status.IsReady() {
		return nil, errors.New("application is not ready")
	}
	var conversation *storage.Conversation
	history := memory.NewChatMessageHistory()
	currentUser, _ := ctx.Value(auth.UserNameContextKey).(string)
	if !req.NewChat {
		search := []storage.SearchOption{
			storage.WithAppName(req.APPName),
			storage.WithAppNamespace(req.AppNamespace),
			storage.WithDebug(req.Debug),
		}
		if currentUser != "" {
			search = append(search, storage.WithUser(currentUser))
		}
		conversation, err = cs.Storage().FindExistingConversation(req.ConversationID, search...)
		if err != nil {
			return nil, err
		}
		for _, v := range conversation.Messages {
			_ = history.AddUserMessage(ctx, v.Query)
			_ = history.AddAIMessage(ctx, v.Answer)
		}
	} else {
		conversation = &storage.Conversation{
			ID:           req.ConversationID,
			AppName:      req.APPName,
			AppNamespace: req.AppNamespace,
			StartedAt:    time.Now(),
			UpdatedAt:    time.Now(),
			Messages:     make([]storage.Message, 0),
			User:         currentUser,
			Debug:        req.Debug,
		}
	}
	conversation.Messages = append(conversation.Messages, storage.Message{
		ID:     messageID,
		Query:  req.Query,
		Answer: "",
	})
	ctx = base.SetAppNamespace(ctx, req.AppNamespace)
	appRun, err := appruntime.NewAppOrGetFromCache(ctx, c, app)
	if err != nil {
		return nil, err
	}
	klog.FromContext(ctx).Info("begin to run application", "appName", req.APPName, "appNamespace", req.AppNamespace)
	out, err := appRun.Run(ctx, c, respStream, appruntime.Input{Question: req.Query, NeedStream: req.ResponseMode.IsStreaming(), History: history})
	if err != nil {
		return nil, err
	}

	conversation.UpdatedAt = time.Now()
	conversation.Messages[len(conversation.Messages)-1].Answer = out.Answer
	conversation.Messages[len(conversation.Messages)-1].References = out.References
	if err := cs.Storage().UpdateConversation(conversation); err != nil {
		return nil, err
	}
	return &ChatRespBody{
		ConversationID: conversation.ID,
		MessageID:      messageID,
		Message:        out.Answer,
		CreatedAt:      time.Now(),
		References:     out.References,
	}, nil
}

func (cs *ChatServer) ListConversations(ctx context.Context, req APPMetadata) ([]storage.Conversation, error) {
	currentUser, _ := ctx.Value(auth.UserNameContextKey).(string)
	return cs.Storage().ListConversations(storage.WithAppNamespace(req.AppNamespace), storage.WithAppName(req.APPName), storage.WithUser(currentUser), storage.WithUser(currentUser))
}

func (cs *ChatServer) DeleteConversation(ctx context.Context, conversationID string) error {
	currentUser, _ := ctx.Value(auth.UserNameContextKey).(string)
	return cs.Storage().Delete(storage.WithConversationID(conversationID), storage.WithUser(currentUser))
}

func (cs *ChatServer) ListMessages(ctx context.Context, req ConversationReqBody) (storage.Conversation, error) {
	currentUser, _ := ctx.Value(auth.UserNameContextKey).(string)
	c, err := cs.Storage().FindExistingConversation(req.ConversationID, storage.WithAppNamespace(req.AppNamespace), storage.WithAppName(req.APPName), storage.WithAppNamespace(req.AppNamespace), storage.WithUser(currentUser))
	if err != nil {
		return storage.Conversation{}, err
	}
	if c != nil {
		return *c, nil
	}
	return storage.Conversation{}, errors.New("conversation is not found")
}

func (cs *ChatServer) GetMessageReferences(ctx context.Context, req MessageReqBody) ([]retriever.Reference, error) {
	currentUser, _ := ctx.Value(auth.UserNameContextKey).(string)
	m, err := cs.Storage().FindExistingMessage(req.ConversationID, req.MessageID, storage.WithAppNamespace(req.AppNamespace), storage.WithAppName(req.APPName), storage.WithAppNamespace(req.AppNamespace), storage.WithUser(currentUser))
	if err != nil {
		return nil, err
	}
	if m != nil && m.References != nil {
		return m.References, nil
	}
	return nil, errors.New("conversation or message is not found")
}

// todo Reuse the flow without having to rebuild req same, not finish, Flow doesn't start with/contain nodes that depend on incomingInput.question