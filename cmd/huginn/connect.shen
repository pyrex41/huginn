(define huginn.step
  idle start -> [listing list]
  listing selected -> [connecting attach]
  listing failed -> [disconnected retry]
  listing quit -> [done stop]
  connecting detached -> [done stop]
  connecting failed -> [disconnected retry]
  disconnected reconnect -> [listing list]
  disconnected quit -> [done stop]
  _ _ -> [done invalid])

(define huginn.drive
  State -> (let Change (huginn.step State (read))
                Next (hd Change)
                Effect (hd (tl Change))
                Printed (output "huginn:~A~%" Effect)
                (if (= Next done) done (huginn.drive Next))))

(huginn.drive idle)
