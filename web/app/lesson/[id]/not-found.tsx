import Link from "next/link";

import RootNotFound from "../../not-found";
import { buttonClass } from "../../ui/button";
import { EmptyState } from "../../ui/empty";
import UserBar from "../../user-bar";
import { getMe } from "./requests";

// Урока нет (notFound() страницы урока): человек вошёл, поэтому 404 — в
// каркасе приложения, с путём к урокам. /auth/me — тот же ответ, что уже
// получила страница (cache()); без сессии — общая 404 под логотипом.
export default async function LessonNotFound() {
  const me = await getMe().catch(() => null);
  if (!me) return <RootNotFound />;
  return (
    <>
      <UserBar name={me.name} role={me.role} />
      <main className="page page--narrow">
        <div className="card">
          <EmptyState
            icon="search"
            title="Страница не найдена"
            titleAs="h1"
            action={
              <Link href="/lessons" className={buttonClass({ variant: "primary" })}>
                К урокам
              </Link>
            }
          >
            Такого урока нет: возможно, ссылка устарела или в адресе опечатка.
          </EmptyState>
        </div>
      </main>
    </>
  );
}
