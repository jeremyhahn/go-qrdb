// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package secure

// The blank import below pins github.com/awnumar/memguard as a DIRECT
// require in this SDK module's go.mod. Downstream SDK consumers must
// see memguard in their go.sum transitively — this enforces that the
// audit-trail for secure-memory provenance travels with the SDK.
//
// The types exported by this package are all aliases; no direct
// memguard APIs are used. The blank import exists solely to affect
// module dependency resolution and is intentional.
import _ "github.com/awnumar/memguard"
