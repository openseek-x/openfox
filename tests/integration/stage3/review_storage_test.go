package stage3

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"openfox/internal/api"
	"openfox/internal/artifacts"
	"openfox/internal/docs"
	"strings"
	"testing"
)

type reviewFullStorage struct{ artifacts.Local }

func (reviewFullStorage) FreeBytes(context.Context) (uint64, error) { return 0, nil }

func TestReviewDocumentAttachmentQuotas(t *testing.T) {
	for _, kind := range []string{"count", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			env := newS3Env(t)
			withAvatarStorage(env, t.TempDir())
			manager := env.manager()
			space := env.space.ID
			box := docInSet(env.newDocSet(manager, space, "配额回归").ID)
			node := env.docNodeIn(manager, space, box, "", docs.KindDoc, "节点")
			count, size := 100, 1
			if kind == "bytes" {
				count, size = 64, 1<<24
			}
			env.exec(`INSERT INTO openfox.doc_attachments (organization_id,id,node_id,name,file_key,content_type,size_bytes,sha256,created_by)
 SELECT $1::uuid,gen_random_uuid(),$2::uuid,'已有附件','docs/review/'||n,'application/octet-stream',$3,repeat('a',64),$4::uuid FROM generate_series(1,$5) n`, env.org, node.ID, size, s3ManagerID, count)
			response := uploadDocAttachment(t, manager, space, node.ID, "超限.txt", []byte("新附件"))
			if response.Status != 409 {
				t.Fatalf("%s 限额未生效：%d %s", kind, response.Status, response.Body)
			}
		})
	}
}

func TestReviewDocumentUploadsRejectLowDisk(t *testing.T) {
	env := newS3Env(t)
	env.server = httptestServer(env, api.NewWithDispatch(env.auth, env.database, false, reviewFullStorage{artifacts.Local{Root: t.TempDir()}}, api.DefaultAuthRateLimits, api.DispatchOptions{}))
	env.clients = map[string]*s3Client{}
	manager := env.manager()
	space := env.space.ID
	box := docInSet(env.newDocSet(manager, space, "水位回归").ID)
	node := env.docNodeIn(manager, space, box, "", docs.KindDoc, "节点")
	for _, response := range []s3Response{
		uploadDocFile(t, manager, space, map[string]string{"container": "set", "set_id": box["set_id"].(string)}, "file.bin", []byte("文件")),
		uploadDocAttachment(t, manager, space, node.ID, "attachment.bin", []byte("附件")),
	} {
		if response.Status != 413 {
			t.Errorf("磁盘水位未阻止上传：%d %s", response.Status, response.Body)
		}
	}
}

func TestReviewAvatarAndIconBoundWholeMultipartBody(t *testing.T) {
	env := newS3Env(t)
	withAvatarStorage(env, t.TempDir())
	client := env.owner()
	for _, route := range []struct{ path, method string }{{"/v1/me/avatar", http.MethodPut}, {"icons", http.MethodPost}} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := writer.WriteField("ignored", strings.Repeat("x", 7<<20)); err != nil {
			t.Fatal(err)
		}
		part, err := writer.CreateFormFile("file", "image.png")
		if err != nil {
			t.Fatal(err)
		}
		part.Write(imageBytes(pngHeader, 128))
		writer.Close()
		response := client.raw(route.method, route.path, &body, writer.FormDataContentType())
		if response.Status != 413 {
			t.Errorf("%s 接受超大非文件字段：%d %s", route.path, response.Status, response.Body)
		}
	}
}
