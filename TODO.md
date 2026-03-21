These tasks should be completed by Claude in priority order. Check them off the list as they're completed.

- [ ] Gallery
  - [x] Show spinner when waiting on generating a thumbnail
  - [x] Pre-generate thumbnails during scan
  - [x] Show scan status on user's screen
  - [x] Allow clicking on the main picture to see a full-sized, zoomable version
  - [x] Hide the face boxes until hover on the image
  - [x] In the list of faces, show the face preview instead of just the names
  - [x] Find duplicate photos and allow bulk delete operations
    - Configurable algorithm/tool in the admin area
    - Display fingerprint in photo view
    - Configurable distance in admin area
    - New section in Gallery that lists the duplicate photos, can click to view duplicates
    - From the duplicates view, can select the keep one or unmark one as duplicate
    - Pre-selects the keep image based on the highest resolution and oldest if there's a tie.
    - Includes a button to delete the non-keep duplicates
- [ ] Move testing snapshots to git-lfs
- [ ] News
  - [ ] Don't mark read until you move off of the article. That fixes the gray out while trying to read and accidental reads.
- [ ] Multifactor authentication
  - [ ] TOTP tokens
  - [ ] OAuth
  - [ ] Email password resets
  - [ ] Session invalidation on password change
- [ ] Admin
  - [ ] Move email to own settings section and allow configuring Go templates with the layout.
  - [ ] Create common component for creating/editing a volume to use for admin area and user's volume; make inputs appear based on connection type.
- [ ] Handle zip/tar files
  - Allow browsing the contents like a regular folder (updates for zip)
  - Allow extracting contents
- [ ] File versioning
  - git-tracked files/directories
  - all files are ignored by default, unless tracking is enabled
- [ ] Backup plugin with Restic REST API
- [ ] Plugin installation and permissions
  - [ ] Enforce DB_SANDBOX, DB_READ, FS_READ, ROUTE_GLOBAL_BIND, etc
  - [ ] Audit and minimize plugin API
  - [ ] Enable trusted protobuf plugins
  - [ ] Allow admins to install trusted plugins
  - [ ] Allow users to install external plugins for themselves
  - [ ] SQLite methods for read_file(), head_file(), file_exists(), file_size()
- [ ] Federate sharing (with NextCloud and Cog)
- [ ] Allow import from NextCloud via API (for individual user) / Database (for instance)
- [ ] Audit log activities to table and expose it via WebDAV
- [ ] Plugin: Deck
- [ ] Plugin: NextPod

---

Building a family photo app in Go with offline, high-quality face recognition is a great challenge. Since you are okay with slower processing in exchange for accuracy on commodity hardware, you should move away from older methods like Haar Cascades and instead use Deep Feature Embeddings via ONNX Runtime.

For the "best quality" requirement, specifically regarding children aging, the current gold standard is ArcFace (Additive Angular Margin Loss).

1. The Recommended Tech Stack
   To keep this in Go while maintaining elite accuracy, use the following combination:

Inference Engine: onnxruntime-go (Microsoft's official C-wrapper for Go). This allows you to run state-of-the-art models without a heavy Python dependency.

Recognition Model: ArcFace (InsightFace buffalo_l or w600k_r50). These models generate a 512-dimension vector (embedding) for every face.
+1

Detection Model: SCRFD. It is significantly more accurate and faster than older models like MTCNN, especially for small faces or varied angles in family photos.

Database: A simple vector store (like milvus-proto or even a local FAISS index via CGO) to store and compare these 512-D vectors.

2. Handling "Aging Up" in Children
   No single model can perfectly match a 2-year-old to their 15-year-old self in a single jump because cranial growth is non-linear. To solve this in a "Family App" context, you should use Template Updating (Continuous Enrollment):

The Cluster Strategy: Instead of matching a new photo against one "Reference Image," you match it against a cluster of images for that person.

The "Bridge" Effect: As you add photos over time (e.g., every 6 months), the app uses the most recent high-confidence matches to "bridge" the gap. Even if a 10-year-old doesn't match their 2-year-old self, they will match their 9-year-old self, who in turn matches the 8-year-old self, and so on.

Manual Refinement: When the recognition confidence drops below a certain threshold (e.g., 0.4 cosine similarity), the app should prompt the user: "Is this still [Child Name]?" Once confirmed, that new embedding becomes the new "anchor" for that age.

3. Implementation Workflow in Go
   Face Detection: Use an ONNX version of SCRFD to find faces and 5 facial landmarks (eyes, nose, mouth corners).

Alignment: Use the landmarks to "warp" the face so the eyes are level and centered. This is the most critical step for quality.

Embedding: Pass the aligned face (112×112 pixels) through the ArcFace model. It will output a []float32 of size 512.

Comparison: Use Cosine Similarity to compare the new vector against your database of known family members.

Similarity=
∥A∥∥B∥
A⋅B
​

4. Comparison of Go Libraries
   Library Accuracy Complexity Best For...
   onnxruntime-go Highest High (needs CGO) The "Gold Standard" approach you are looking for.
   go-face High Medium Based on dlib. Very accurate but harder to customize for specific "age-invariant" models.
   pigo Low Low (Pure Go) Fast face detection only; not recommended for high-quality recognition.
   Next Steps
   Would you like me to provide a Go code snippet for calculating the cosine similarity between two face vectors, or perhaps a guide on where to find the pre-trained ArcFace ONNX models?
