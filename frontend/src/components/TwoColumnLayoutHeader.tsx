type Props = {
  title: string;
  description: React.ReactNode;
};

export default function TwoColumnLayoutHeader({ title, description }: Props) {
  return (
    <div className="grid gap-2 text-left">
      <h1 className="text-2xl font-semibold">{title}</h1>
      <p className="text-muted-foreground">{description}</p>
    </div>
  );
}
