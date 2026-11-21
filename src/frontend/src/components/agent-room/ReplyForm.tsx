import { Button } from "@/components/ui/button";
import { useSubmitBotReply } from "@/hooks/useRoomQueries";
import { useState } from "react";

interface ReplyFormProps {
  token: string;
  turnSeq: bigint;
  onSubmitted: () => void;
}

export function ReplyForm({ token, turnSeq, onSubmitted }: ReplyFormProps) {
  const [text, setText] = useState("");
  const submit = useSubmitBotReply(token);
  const trimmed = text.trim();

  const handleSubmit = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (trimmed.length === 0 || submit.isPending) return;
    submit.mutate(
      { expectedTurnSeq: turnSeq, content: trimmed },
      {
        onSuccess: (accepted) => {
          if (accepted) {
            setText("");
            onSubmitted();
          }
        },
      },
    );
  };

  return (
    <form
      data-ocid="room.reply.panel"
      onSubmit={handleSubmit}
      className="border-2 border-foreground bg-card px-4 py-4"
    >
      <label
        htmlFor="room-reply"
        className="block font-mono text-sm font-bold text-foreground"
      >
        Your Reply (Turn {turnSeq.toString()})
      </label>
      <textarea
        id="room-reply"
        data-ocid="room.reply.textarea"
        value={text}
        onChange={(event) => setText(event.target.value)}
        rows={6}
        disabled={submit.isPending}
        className="mt-2 block w-full resize-y border-2 border-foreground bg-background p-3 font-body text-base text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:opacity-70"
      />
      <Button
        type="submit"
        data-ocid="room.reply.submit_button"
        disabled={submit.isPending || trimmed.length === 0}
        className="mt-3 h-12 w-full rounded-none border-2 border-foreground bg-foreground text-base font-extrabold uppercase tracking-wider text-background hover:bg-foreground/85 disabled:opacity-60"
      >
        {submit.isPending ? "Sending..." : "Send Reply"}
      </Button>
    </form>
  );
}
