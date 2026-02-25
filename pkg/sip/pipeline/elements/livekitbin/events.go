package livekitbin

// func (e *LivekitBin) HandleMessage(self *gst.Bin, msg *gst.Message) {
// 	switch msg.Type() {
// 	case gst.MessageElement:
// 		structure := msg.GetStructure()
// 		if structure == nil {
// 			return
// 		}
// 		switch structure.Name() {
// 		case "LivekitTrackUnsubscribed":
// 			self.Log(CAT, gst.LevelDebug, "Received LivekitTrackUnsubscribed message")
// 			glib.IdleAdd(e.OnTrackUnsubscribed, self, gst.ToElement(msg.SourceObject()))
// 		}
// 	}
// }

// func (e *LivekitBin) OnTrackUnsubscribed(self *gst.Bin, src *gst.Element) {
// 	self.Log(CAT, gst.LevelDebug, "Handling LivekitTrackUnsubscribed message")

// }
